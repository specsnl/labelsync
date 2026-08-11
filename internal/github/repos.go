// repos.go turns the selectors config resolved into concrete repositories.
//
// # Filtering is free and stays free
//
// A repository listing already carries archived, fork, private, and has_issues
// on every entry, so every filter is answered from the enumeration response.
// Nothing here issues a per-repository GET to check an attribute: that would
// turn one request per hundred repositories into one per repository, for
// information already in hand.
//
// The filters themselves live in config.Selector.Reject, offline and testable
// without an HTTP mock. This file's job is to produce the config.Repo values it
// judges — and, for `labelsync groups`, to keep what it rejected and why.
package github

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	gogithub "github.com/google/go-github/v76/github"
	"golang.org/x/sync/errgroup"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// reposPerPage is the maximum GitHub allows, and the whole cost model: fifty
// repositories are one request, not fifty.
const reposPerPage = 100

// defaultConcurrency bounds the parallel enumeration walks when the caller asks
// for nothing in particular. It mirrors cmd.DefaultConcurrency, which is the
// user-facing default behind --concurrency; this is only the fallback for a
// caller that passes zero.
const defaultConcurrency = 8

// Rejected is a repository a selector listed and then filtered out, with the
// reason it was.
//
// It is carried rather than discarded because the absence of an expected
// repository is exactly what `labelsync groups` exists to explain. Nothing else
// reads it: enumeration's answer is [Selection.Repos].
type Rejected struct {
	Repo   config.Repo
	Reason string
}

// Selection is one selector's enumeration: the repositories it selects, and the
// ones it listed and filtered out.
type Selection struct {
	Selector config.Selector

	// Repos are the selected repositories, in the order the API listed them.
	Repos []config.Repo

	// Rejected are the repositories the listing returned that the selector's
	// filters removed. It is always empty for a repos selector: nothing is
	// enumerated for one, so nothing can be filtered out of it.
	Rejected []Rejected
}

// Select walks every selector and returns what each one resolved to, in the
// order the selectors were given.
//
// This is the per-group answer. [Client.Enumerate] is the union of it, and is
// what a run that only needs "which repositories" should call.
//
// Selectors are walked in parallel, bounded by concurrency — non-positive means
// [defaultConcurrency]. Reads are not subject to the content-creation secondary
// limit, so the bound is politeness and round-trip latency rather than a quota
// concern.
//
// An owner that cannot be listed is recorded and skipped, not fatal: its
// selection comes back empty. One mistyped org in a config that names four
// should report itself at the end of the run rather than take the other three
// down with it. Anything else — a 401, a cancelled context — ends the run.
func (c *Client) Select(ctx context.Context, selectors []config.Selector, concurrency int) ([]Selection, error) {
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}

	// Indexed rather than appended: the results keep the caller's order, which
	// is the group order a report reads down, and parallel appends would give
	// whatever order the walks happened to finish in.
	out := make([]Selection, len(selectors))

	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(concurrency)

	for i, selector := range selectors {
		group.Go(func() error {
			selection, err := c.selectorRepos(ctx, selector)
			if err != nil {
				// Already recorded by Do, and the summary will name it. Every
				// other error is the run's.
				if errors.Is(err, labelsync.ErrRepoInaccessible) {
					out[i] = Selection{Selector: selector}

					return nil
				}

				return err
			}

			out[i] = selection

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	return out, nil
}

// Enumerate walks every selector and returns the distinct repositories they
// select, sorted by owner and then name.
//
// The union is deduplicated case-insensitively: two groups naming the same
// repository is ordinary — it is how a repository ends up with the labels of
// both — and enumerating it twice would plan it twice.
func (c *Client) Enumerate(ctx context.Context, selectors []config.Selector, concurrency int) ([]config.Repo, error) {
	selections, err := c.Select(ctx, selectors, concurrency)
	if err != nil {
		return nil, err
	}

	out := Union(selections)

	slog.Debug("enumeration complete", "selectors", len(selectors), "repositories", len(out))

	return out, nil
}

// Union deduplicates the repositories of several selections, case-insensitively,
// and sorts them by owner and then name.
//
// Sorted because a map is not, and because every downstream artefact — the diff,
// the JSON stream, the golden files — is compared between runs.
func Union(selections []Selection) []config.Repo {
	found := make(map[string]config.Repo)

	for _, selection := range selections {
		for _, repo := range selection.Repos {
			// First spelling wins, which makes the answer the caller's selector
			// order rather than whichever parallel walk wrote last. GitHub
			// addresses Owner/Repo and owner/repo as one repository, so which of
			// them is carried has to be decided by something stable.
			key := strings.ToLower(repo.String())
			if _, seen := found[key]; !seen {
				found[key] = repo
			}
		}
	}

	out := make([]config.Repo, 0, len(found))
	for _, repo := range found {
		out = append(out, repo)
	}

	slices.SortFunc(out, compareRepos)

	return out
}

// compareRepos orders repositories by owner and then name.
func compareRepos(a, b config.Repo) int {
	if owner := strings.Compare(a.Owner, b.Owner); owner != 0 {
		return owner
	}

	return strings.Compare(a.Name, b.Name)
}

// selectorRepos lists one selector's repositories and splits them into the ones
// it selects and the ones its filters removed.
//
// An explicit repos: entry passes through untouched and unenumerated. The config
// named the repository outright, so there is nothing to filter and nothing worth
// a request: a filter that silently dropped a repository the config asked for by
// name would be a surprise rather than a safety net. Archived, Fork, Private and
// HasIssues are consequently unknown — false — for these, which is why nothing
// downstream may treat them as authoritative for an explicit entry.
func (c *Client) selectorRepos(ctx context.Context, selector config.Selector) (Selection, error) {
	if selector.Kind == config.SourceRepos {
		return Selection{Selector: selector, Repos: slices.Clone(selector.Repos)}, nil
	}

	listed, err := c.listRepos(ctx, selector)
	if err != nil {
		return Selection{}, err
	}

	selection := Selection{Selector: selector}

	for _, repo := range listed {
		if reason := selector.Reject(repo); reason != "" {
			selection.Rejected = append(selection.Rejected, Rejected{Repo: repo, Reason: reason})

			continue
		}

		selection.Repos = append(selection.Repos, repo)
	}

	return selection, nil
}

// listRepos walks every page of the endpoint the selector's kind implies.
//
// The three endpoints are not interchangeable. An org listing is the ordinary
// case. For a user, GET /user/repos?affiliation=owner is the only one that
// returns private repositories, and it only exists for the token's own user;
// GET /users/{user}/repos works for anyone and returns public repositories only.
// Which of the two applies was decided in config.Resolve, from the authenticated
// login, so this file does not go and ask.
func (c *Client) listRepos(ctx context.Context, selector config.Selector) ([]config.Repo, error) {
	var (
		out  []config.Repo
		opts = gogithub.ListOptions{PerPage: reposPerPage}
	)

	for {
		var page []*gogithub.Repository

		err := c.Do(ctx, selector.Owner, "list repositories", func(ctx context.Context) (*gogithub.Response, error) {
			var (
				resp *gogithub.Response
				err  error
			)

			page, resp, err = c.listReposPage(ctx, selector, opts)

			if resp != nil {
				opts.Page = resp.NextPage
			}

			return resp, err
		})
		if err != nil {
			return nil, err
		}

		for _, repo := range page {
			out = append(out, config.Repo{
				Owner:    repo.GetOwner().GetLogin(),
				Name:     repo.GetName(),
				Archived: repo.GetArchived(),
				Fork:     repo.GetFork(),
				Private:  repo.GetPrivate(),

				// Carried, never filtered on. This is the only place the value
				// enters the program, and the only place it is ever known: a
				// repository the config named outright was not enumerated, so
				// its flag stays nil rather than defaulting to a note nobody
				// checked.
				HasIssues: new(repo.GetHasIssues()),
			})
		}

		if opts.Page == 0 {
			return out, nil
		}
	}
}

// listReposPage issues one page of whichever listing the selector calls for.
//
// The authenticated-user request sets affiliation and nothing else: GitHub
// rejects a request that carries both affiliation and type, and affiliation=owner
// is the narrower of the two — repositories the user owns, rather than every one
// they can see through an organisation or a collaborator invitation.
func (c *Client) listReposPage(
	ctx context.Context,
	selector config.Selector,
	opts gogithub.ListOptions,
) ([]*gogithub.Repository, *gogithub.Response, error) {
	switch {
	case selector.Kind == config.SourceOrg:
		return c.rest.Repositories.ListByOrg(ctx, selector.Owner, &gogithub.RepositoryListByOrgOptions{
			ListOptions: opts,
		})

	case selector.AuthenticatedUser:
		return c.rest.Repositories.ListByAuthenticatedUser(ctx, &gogithub.RepositoryListByAuthenticatedUserOptions{
			Affiliation: "owner",
			ListOptions: opts,
		})

	default:
		return c.rest.Repositories.ListByUser(ctx, selector.Owner, &gogithub.RepositoryListByUserOptions{
			ListOptions: opts,
		})
	}
}
