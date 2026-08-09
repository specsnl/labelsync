// labels.go holds the four label operations and nothing else. Every one of them
// goes through [Client.Do], so a repository that cannot be reached is recorded
// and skipped rather than ending the run.
//
// # Why a local Label type
//
// [Label] mirrors [plan.Label] field for field, on purpose: the planner takes
// plain structs and declares its own, which is what keeps internal/plan free of
// internal/github. Identical names, types, and order make the two directly
// convertible, so the call site that bridges them writes plan.Label(l) rather
// than a mapping function — and the compiler notices if either side drifts. The
// dependency stays pointing one way, and neither package imports the other.
package github

import (
	"context"
	"fmt"
	"net/url"

	gogithub "github.com/google/go-github/v76/github"
)

// labelsPerPage is the maximum GitHub allows. More than 100 labels in one
// repository is rare, but a truncated list would read as "these labels do not
// exist" and plan their creation, so the list is always walked to the end.
const labelsPerPage = 100

// Label is a label as a repository holds it today.
//
// It is deliberately convertible to [plan.Label] — same fields, same order — so
// that translating an API response into planner input costs a conversion rather
// than a mapping function. See the package-level note in this file.
type Label struct {
	// Name is the name exactly as the repository stores it, casing included.
	// Matching against the config is case-insensitive, but the stored casing is
	// what an update has to correct.
	Name string

	// Color is six-digit hex, as GitHub stores it: no leading #.
	Color string

	// Description is the description, empty when there is none. GitHub does not
	// distinguish an absent description from an empty one, and neither does this.
	Description string
}

// ListLabels returns every label in the repository, walking every page.
func (c *Client) ListLabels(ctx context.Context, owner, repo string) ([]Label, error) {
	var (
		labels []Label
		opts   = &gogithub.ListOptions{PerPage: labelsPerPage}
	)

	for {
		var page []*gogithub.Label

		err := c.Do(ctx, slug(owner, repo), "list labels", func(ctx context.Context) (*gogithub.Response, error) {
			var (
				resp *gogithub.Response
				err  error
			)

			page, resp, err = c.rest.Issues.ListLabels(ctx, owner, repo, opts)

			// The next page is read off the response, so the cursor has to move
			// before the classification below can return.
			if resp != nil {
				opts.Page = resp.NextPage
			}

			return resp, err
		})
		if err != nil {
			return nil, err
		}

		for _, l := range page {
			labels = append(labels, Label{
				Name:        l.GetName(),
				Color:       l.GetColor(),
				Description: l.GetDescription(),
			})
		}

		if opts.Page == 0 {
			return labels, nil
		}
	}
}

// CreateLabel creates label in the repository.
//
// A 422 already_exists is **not** a failure and is reclassified as an update:
// the label is already there under some casing, and the desired end state is a
// label carrying the configured values, which the update reaches. Two ordinary
// situations produce it — a plan computed against state that has since changed,
// and two runs overlapping — and case-only drift is a third, because GitHub
// holds label names case-insensitively unique.
//
// The update addresses the label by the **desired** name rather than by the one
// the repository holds, because a create has no observed name to work from. That
// is safe: label lookup is case-insensitive, so the path resolves to the `bug`
// that rejected `Bug`, and new_name then corrects the casing in the same call.
func (c *Client) CreateLabel(ctx context.Context, owner, repo string, label Label) error {
	err := c.Do(ctx, slug(owner, repo), "create label", func(ctx context.Context) (*gogithub.Response, error) {
		_, resp, err := c.rest.Issues.CreateLabel(ctx, owner, repo, &gogithub.Label{
			Name:  new(label.Name),
			Color: new(label.Color),

			// Sent even when empty. go-github's Description is a pointer with
			// omitempty, so leaving it nil would drop the field and let a stale
			// description survive a create-turned-update.
			Description: new(label.Description),
		})

		return resp, err
	})

	if err != nil && IsAlreadyExists(err) {
		return c.UpdateLabel(ctx, owner, repo, label.Name, label)
	}

	return err
}

// UpdateLabel patches the label the repository holds as current so that it
// carries the values in label.
//
// A rename is a PATCH carrying new_name and never a delete plus a create,
// because new_name **preserves** every issue and pull-request association and
// keeps the label's id. Deleting and recreating would strip the label from every
// issue that used it — the same damage as [Client.DeleteLabel], for a rename
// nobody asked to be destructive.
//
// current is the name the repository was observed to hold, so the request stays
// consistent with the state the plan was computed against; label.Name is the
// desired spelling and is always sent as new_name. Sending one identical to the
// path is a no-op on GitHub's side, which keeps recolours and renames one code
// path rather than two.
//
// The request is built here rather than through go-github's EditLabel, which
// sends the label's `name` field. GitHub's update endpoint reads **new_name**
// and ignores `name`, so EditLabel would return a cheerful 200 having renamed
// nothing.
func (c *Client) UpdateLabel(ctx context.Context, owner, repo, current string, label Label) error {
	body := struct {
		NewName     string `json:"new_name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}{
		NewName: label.Name,
		Color:   label.Color,

		// Descriptions are authoritative, so the field is always sent. Omitting
		// it would leave a stale description in place on a label the config says
		// has none.
		Description: label.Description,
	}

	return c.Do(ctx, slug(owner, repo), "update label", func(ctx context.Context) (*gogithub.Response, error) {
		req, err := c.rest.NewRequest("PATCH", labelPath(owner, repo, current), body)
		if err != nil {
			return nil, fmt.Errorf("building the update request: %w", err)
		}

		return c.rest.Do(ctx, req, nil)
	})
}

// DeleteLabel removes the label from the repository.
//
// **This is destructive beyond the label itself**: GitHub removes it from every
// issue and pull request that carried it, and nothing restores that. Call it
// only in prune mode, on a candidate the user has been shown and has accepted —
// the planner emits removal candidates and never decides which of them are
// deleted, and this function is the reason that separation exists.
func (c *Client) DeleteLabel(ctx context.Context, owner, repo, name string) error {
	return c.Do(ctx, slug(owner, repo), "delete label", func(ctx context.Context) (*gogithub.Response, error) {
		req, err := c.rest.NewRequest("DELETE", labelPath(owner, repo, name), nil)
		if err != nil {
			return nil, fmt.Errorf("building the delete request: %w", err)
		}

		return c.rest.Do(ctx, req, nil)
	})
}

// labelPath builds the path of one label, escaping the name.
//
// The escaping is not decoration: GitHub label names may contain a slash —
// `area/api` is a common convention — and interpolating one raw would address a
// different resource entirely. Owner and repository names cannot, having been
// through config.ParseRepoRef's character set.
func labelPath(owner, repo, name string) string {
	return fmt.Sprintf("repos/%s/%s/labels/%s", owner, repo, url.PathEscape(name))
}

// slug renders owner/repo, which is how a repository is named in errors and in
// the end-of-run summary.
func slug(owner, repo string) string { return owner + "/" + repo }
