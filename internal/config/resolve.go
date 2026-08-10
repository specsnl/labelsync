package config

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/danwakefield/fnmatch"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// repoNamePattern matches one half of an owner/repo reference. It is GitHub's
// own character set, which is deliberately narrower than "anything but a slash":
// a space or a colon inside a name is a typo worth catching at load rather than
// a 404 halfway through a run. A dot is not — `docs.example.com` is a perfectly
// ordinary repository name, which is also why a trailing ".git" cannot be told
// apart from a name and is taken literally.
var repoNamePattern = regexp.MustCompile(`^[\w.-]+$`)

// SourceKind names where a selector's repositories come from. There is no kind
// for include_groups: composition is flattened away during resolution, so what
// a composed group leaves behind is the selectors of the groups it includes.
type SourceKind string

// The three selector kinds an enumerator has to know how to walk.
const (
	SourceOrg   SourceKind = "org"
	SourceUser  SourceKind = "user"
	SourceRepos SourceKind = "repos"
)

// The source names as the config file spells them, for error messages and for
// the "exactly one" check.
const (
	sourceOrg           = "org"
	sourceUser          = "user"
	sourceRepos         = "repos"
	sourceIncludeGroups = "include_groups"
)

// Repo is one repository, in as much detail as a selector looks at. It is a
// plain struct on purpose: the enumerator in internal/github fills one in per
// repository it sees, and every membership question is answered here, offline.
type Repo struct {
	Owner string
	Name  string

	// Archived, Fork, and Private are only consulted for org and user
	// selectors. An explicit repos entry names a repository outright, and a
	// filter that silently dropped a repository the config asked for by name
	// would be a surprise rather than a safety net.
	Archived bool
	Fork     bool
	Private  bool

	// HasIssues is whether the repository has issues enabled, and nil when that
	// is not known. It is **not** a filter and nothing skips on it: the GH-17
	// spike confirmed that repository-scoped label endpoints are ungated on it,
	// so such a repository syncs normally and its labels are genuinely used by
	// pull requests.
	//
	// It is carried because the diff notes it — label changes on a repository
	// with issues off are surprising enough that a reader would otherwise
	// suspect the config or the group filter. Filtering those repositories out
	// stays the user's choice, through the group filters.
	//
	// The pointer is what keeps "not known" from rendering as "disabled". An
	// explicit repos entry is never enumerated, so nothing ever saw the flag for
	// one, and a plain bool would put an untrue note on every repository the
	// config names outright.
	HasIssues *bool
}

// String renders the repository as owner/repo.
func (r Repo) String() string {
	return r.Owner + "/" + r.Name
}

// ParseRepoRef splits an owner/repo reference. Anything else — a bare name, a
// URL, an empty half, a space in either half — is ErrInvalidRepoRef.
//
// This is the only place a reference is judged, so a repos entry in the config
// and a --repo on the command line are held to one rule.
//
// Whitespace around either half is trimmed rather than rejected: a human typed
// this into YAML, and `specsnl / labelsync` says which repository it means. A
// space *inside* a half is a different thing and is rejected, because GitHub has
// no such name and the reference cannot be what was meant.
func ParseRepoRef(raw string) (Repo, error) {
	owner, name, found := strings.Cut(raw, "/")

	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)

	if !found || !repoNamePattern.MatchString(owner) || !repoNamePattern.MatchString(name) {
		return Repo{}, fmt.Errorf("%w: %s", labelsync.ErrInvalidRepoRef, raw)
	}

	return Repo{Owner: owner, Name: name}, nil
}

// Selector is one group's source, flattened and defaulted: everything the
// enumerator needs to list repositories, and everything Matches needs to decide
// whether a repository belongs. It is deliberately not a repository list —
// producing one needs the network, which is what keeps this package testable
// without an HTTP mock.
type Selector struct {
	// Group is the group this selector came from. A composed group borrows the
	// selectors of the groups it includes, so this can differ from the group
	// the caller asked about.
	Group string

	Kind  SourceKind
	Owner string // the org or user login; empty for SourceRepos
	Repos []Repo // the parsed owner/repo list; empty for the other kinds

	// Include and Exclude are globs over the repository name only, never
	// owner/repo. Exclude is applied after Include.
	Include []string
	Exclude []string

	SkipArchived bool
	SkipForks    bool
	Visibility   Visibility

	// AuthenticatedUser records which of the two user endpoints enumeration has
	// to call. GET /user/repos?affiliation=owner sees private repositories but
	// only for the token's own user; GET /users/{user}/repos works for anyone
	// and returns public repositories only. The decision needs the
	// authenticated login, which this package must not go and fetch, so it is
	// made once here from the login the caller passes to Resolve.
	AuthenticatedUser bool
}

// Rejection reasons, as `labelsync groups` prints them. They are prose for a
// human — the absence of an expected repository is the thing that command exists
// to explain — and nothing branches on the string.
const (
	rejectedOwner      = "a different owner"
	rejectedArchived   = "archived, and skip_archived is on"
	rejectedFork       = "a fork, and skip_forks is on"
	rejectedNotInclude = "matched by no include glob"
	rejectedExclude    = "matched by an exclude glob"
)

// Matches reports whether repo belongs to this selector. It answers the same
// question the enumerator's filters answer, so a repository listed by the API
// and a repository handed straight to --repo are judged by one rule.
func (s Selector) Matches(repo Repo) bool {
	return s.Reject(repo) == ""
}

// Reject is [Selector.Matches] with its reasoning: "" when repo belongs, and
// otherwise why it does not.
//
// The two are one function rather than two so that the reason a repository was
// filtered out cannot disagree with whether it was. `labelsync groups` prints
// these; enumeration ignores them.
//
// A repos selector rejects with no reason at all — a repository is either named
// or it is not, and "the config does not list it" is not an explanation anyone
// needs.
func (s Selector) Reject(repo Repo) string {
	if s.Kind == SourceRepos {
		named := slices.ContainsFunc(s.Repos, func(want Repo) bool {
			return strings.EqualFold(want.Owner, repo.Owner) && strings.EqualFold(want.Name, repo.Name)
		})

		if named {
			return ""
		}

		return rejectedOwner
	}

	switch {
	case !strings.EqualFold(s.Owner, repo.Owner):
		return rejectedOwner

	case s.SkipArchived && repo.Archived:
		return rejectedArchived

	case s.SkipForks && repo.Fork:
		return rejectedFork

	case !s.Visibility.matches(repo):
		return "visibility: " + string(s.Visibility)

	case len(s.Include) > 0 && !anyGlob(s.Include, repo.Name):
		return rejectedNotInclude

	case anyGlob(s.Exclude, repo.Name):
		return rejectedExclude
	}

	return ""
}

// matches reports whether repo has the visibility this value asks for. An
// unrecognised value never matches: it is validate.go's job to reject one, and
// quietly treating it as "all" would widen a selector the file meant to narrow.
func (v Visibility) matches(repo Repo) bool {
	switch v {
	case VisibilityAll:
		return true
	case VisibilityPublic:
		return !repo.Private
	case VisibilityPrivate:
		return repo.Private
	default:
		return false
	}
}

// Warning is something worth saying about a resolution that is not worth
// failing over. Resolve collects them rather than printing them, because this
// package has no writer and the caller knows whether the run is pretty or JSON.
type Warning struct {
	Group   string
	Message string
}

// String renders a warning as the caller would print it.
func (w Warning) String() string {
	return fmt.Sprintf("group %q: %s", w.Group, w.Message)
}

// Resolution is the network-free answer to "which repositories does each group
// select, and which labels does a given repository want".
type Resolution struct {
	labels   []Label
	groups   map[string][]Selector
	names    []string
	warnings []Warning
}

// Resolve turns the groups section into selectors: one source per group,
// include_groups flattened into the selectors of the groups it names, and every
// filter carried along.
//
// authenticatedUser is the login the run's token belongs to, and may be empty
// when it is not known yet. It decides nothing about membership; it only picks
// which of the two user endpoints a user selector has to use, and whether
// asking for private repositories of that user is going to come back empty.
//
// Resolve checks the group graph and nothing else. A label naming a group that
// does not exist is ErrUnknownGroup from validate.go, not from here — a label
// is not part of the graph, and its group simply matches no repository.
func (c *Config) Resolve(authenticatedUser string) (*Resolution, error) {
	r := &Resolution{
		labels: c.Labels,
		groups: make(map[string][]Selector, len(c.Groups)),
		names:  slices.Sorted(maps.Keys(c.Groups)),
	}

	leaves := make(map[string]Selector, len(c.Groups))

	// Every leaf group first, so composition can be flattened in one pass over
	// a complete map rather than recursing into a group that has not been
	// checked yet.
	for _, name := range r.names {
		group := c.Groups[name]

		sources, err := checkGroupSource(name, group)
		if err != nil {
			return nil, err
		}

		if sources[0] == sourceIncludeGroups {
			continue
		}

		selector, warning, err := leafSelector(name, group, sources[0], authenticatedUser)
		if err != nil {
			return nil, err
		}

		if warning != nil {
			r.warnings = append(r.warnings, *warning)
		}

		leaves[name] = selector
	}

	f := &flattener{
		groups: c.Groups,
		leaves: leaves,
		done:   make(map[string][]Selector, len(c.Groups)),
		open:   make(map[string]bool, len(c.Groups)),
	}

	for _, name := range r.names {
		selectors, err := f.flatten(name, nil)
		if err != nil {
			return nil, err
		}

		r.groups[name] = selectors
	}

	return r, nil
}

// Warnings returns what the resolution wants said out loud, in group order.
func (r *Resolution) Warnings() []Warning {
	return r.warnings
}

// Names returns every group name, sorted.
func (r *Resolution) Names() []string {
	return slices.Clone(r.names)
}

// SelectorsFor returns the selectors a single group resolves to. A composed
// group returns the selectors of every group it reaches, deduplicated; an
// undefined group returns nothing.
func (r *Resolution) SelectorsFor(group string) []Selector {
	return slices.Clone(r.groups[group])
}

// Selectors returns every distinct selector in the config, sorted by the group
// that defined it. This is the enumeration work list: one walk per selector
// covers every group, however many composed groups point at it.
func (r *Resolution) Selectors() []Selector {
	seen := make(map[string]bool, len(r.groups))

	var out []Selector

	for _, name := range r.names {
		for _, selector := range r.groups[name] {
			if seen[selector.Group] {
				continue
			}

			seen[selector.Group] = true

			out = append(out, selector)
		}
	}

	slices.SortFunc(out, func(a, b Selector) int { return strings.Compare(a.Group, b.Group) })

	return out
}

// Matches reports whether group selects repo. A group that does not exist
// matches nothing.
func (r *Resolution) Matches(group string, repo Repo) bool {
	return slices.ContainsFunc(r.groups[group], func(s Selector) bool { return s.Matches(repo) })
}

// Groups returns the names of the groups that select repo, sorted. An empty
// result is the safety property: no group resolves to this repository, so
// nothing about it is ours to touch.
func (r *Resolution) Groups(repo Repo) []string {
	var out []string

	for _, name := range r.names {
		if r.Matches(name, repo) {
			out = append(out, name)
		}
	}

	return out
}

// Desired returns the labels repo should have, in config order: every label
// whose groups contain a group that resolves to repo.
//
// A repository no group resolves to yields no labels. That is not the same as
// an empty config, and callers must keep telling the two apart — an empty
// desired set for an unselected repository means "leave it alone", never
// "delete everything it has".
func (r *Resolution) Desired(repo Repo) []Label {
	matched := r.Groups(repo)
	if len(matched) == 0 {
		return nil
	}

	var out []Label

	for _, label := range r.labels {
		if slices.ContainsFunc(label.Groups, func(g string) bool { return slices.Contains(matched, g) }) {
			out = append(out, label)
		}
	}

	return out
}

// flattener resolves include_groups into leaf selectors, and refuses to do so
// for a cycle.
type flattener struct {
	groups Groups
	leaves map[string]Selector
	done   map[string][]Selector
	open   map[string]bool
}

// flatten returns the selectors group resolves to. path is the chain of
// include_groups that led here, so a cycle can be reported as the chain a
// reader has to break rather than as the single name it was noticed at.
//
// A group already resolved is returned from done: revisiting a group is a
// diamond, not a cycle, and only a group still open in this chain is one.
func (f *flattener) flatten(name string, path []string) ([]Selector, error) {
	if selectors, ok := f.done[name]; ok {
		return selectors, nil
	}

	if f.open[name] {
		return nil, fmt.Errorf("%w: %s", labelsync.ErrCyclicGroup, strings.Join(append(path, name), " -> "))
	}

	if selector, ok := f.leaves[name]; ok {
		f.done[name] = []Selector{selector}

		return f.done[name], nil
	}

	f.open[name] = true
	defer delete(f.open, name)

	var (
		out  []Selector
		seen = make(map[string]bool)
	)

	// Config order, not sorted: the file's order is already deterministic, and
	// it is the order a reader expects the union to be listed in.
	for _, include := range f.groups[name].IncludeGroups {
		if _, ok := f.groups[include]; !ok {
			return nil, fmt.Errorf("%w: group %q includes %q", labelsync.ErrUnknownGroup, name, include)
		}

		selectors, err := f.flatten(include, append(path, name))
		if err != nil {
			return nil, err
		}

		for _, selector := range selectors {
			if seen[selector.Group] {
				continue
			}

			seen[selector.Group] = true

			out = append(out, selector)
		}
	}

	f.done[name] = out

	return out, nil
}

// leafSelector builds the selector for a group that names repositories itself,
// and reports the one thing worth warning about while doing so.
func leafSelector(name string, group Group, source, authenticatedUser string) (Selector, *Warning, error) {
	selector := Selector{Group: name}

	// The filters apply to org and user sources only. A repos entry names a
	// repository outright, so it carries none of them, and nothing downstream
	// has to remember which fields it may read for which kind.
	filtered := Selector{
		Group:        name,
		Include:      group.Include,
		Exclude:      group.Exclude,
		SkipArchived: group.SkipArchived,
		SkipForks:    group.SkipForks,
		Visibility:   group.Visibility,
	}

	switch source {
	case sourceOrg:
		selector = filtered
		selector.Kind = SourceOrg
		selector.Owner = group.Org

	case sourceUser:
		selector = filtered
		selector.Kind = SourceUser
		selector.Owner = group.User
		selector.AuthenticatedUser = authenticatedUser != "" && strings.EqualFold(group.User, authenticatedUser)

		if group.Visibility == VisibilityPrivate && !selector.AuthenticatedUser {
			return selector, &Warning{
				Group: name,
				Message: fmt.Sprintf(
					"visibility: private for user %q, who is not the authenticated user — GitHub only lists that user's public repositories, so this group selects nothing",
					group.User,
				),
			}, nil
		}

	case sourceRepos:
		selector.Kind = SourceRepos

		for _, raw := range group.Repos {
			repo, err := ParseRepoRef(raw)
			if err != nil {
				return Selector{}, nil, fmt.Errorf("group %q: %w", name, err)
			}

			selector.Repos = append(selector.Repos, repo)
		}
	}

	return selector, nil, nil
}

// sourcesOf returns the source keys a group sets, in the order the reference
// table lists them.
func sourcesOf(g Group) []string {
	var out []string

	if g.Org != "" {
		out = append(out, sourceOrg)
	}

	if g.User != "" {
		out = append(out, sourceUser)
	}

	if len(g.Repos) > 0 {
		out = append(out, sourceRepos)
	}

	if len(g.IncludeGroups) > 0 {
		out = append(out, sourceIncludeGroups)
	}

	return out
}

// checkGroupSource enforces exactly one of org, user, repos, or include_groups,
// and returns the one it found. A group that set nothing is as ambiguous as one
// that set three things — neither says which repositories it means — so zero
// sources is the same sentinel as two.
//
// validate.go does not repeat this rule: it reaches it through Resolve, so
// there is one implementation and one message. Two of each is what produced a
// cycle chain printed two different ways.
func checkGroupSource(name string, group Group) ([]string, error) {
	sources := sourcesOf(group)

	switch len(sources) {
	case 1:
		return sources, nil
	case 0:
		return nil, fmt.Errorf("%w: group %q sets none of them", labelsync.ErrAmbiguousGroupSource, name)
	default:
		return nil, fmt.Errorf("%w: group %q sets %s", labelsync.ErrAmbiguousGroupSource, name, strings.Join(sources, " and "))
	}
}

// anyGlob reports whether name matches any of the patterns. Matching is
// case-insensitive because GitHub repository names are: owner/Repo and
// owner/repo address the same repository, so a pattern that told them apart
// would exclude a repository depending on how the API happened to spell it.
func anyGlob(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(pattern string) bool {
		return fnmatch.Match(pattern, name, fnmatch.FNM_CASEFOLD)
	})
}
