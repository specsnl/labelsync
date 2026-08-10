package config_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/labelsync"
)

func TestParseRepoRef(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want config.Repo
		err  error
	}{
		{name: "owner and repo", raw: "specsnl/labelsync", want: config.Repo{Owner: "specsnl", Name: "labelsync"}},
		{name: "surrounding space is trimmed", raw: "  specsnl / labelsync  ", want: config.Repo{Owner: "specsnl", Name: "labelsync"}},
		{name: "a bare name has no owner", raw: "labelsync", err: labelsync.ErrInvalidRepoRef},
		{name: "an empty owner", raw: "/labelsync", err: labelsync.ErrInvalidRepoRef},
		{name: "an empty repo", raw: "specsnl/", err: labelsync.ErrInvalidRepoRef},
		{name: "a URL is not a reference", raw: "https://github.com/specsnl/labelsync", err: labelsync.ErrInvalidRepoRef},
		{name: "a space inside the repo half", raw: "specsnl/label sync", err: labelsync.ErrInvalidRepoRef},
		{name: "a space inside the owner half", raw: "specs nl/labelsync", err: labelsync.ErrInvalidRepoRef},
		{name: "a trailing .git is not stripped", raw: "specsnl/labelsync.git", want: config.Repo{Owner: "specsnl", Name: "labelsync.git"}},
		{name: "empty", raw: "", err: labelsync.ErrInvalidRepoRef},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := config.ParseRepoRef(test.raw)

			if test.err != nil {
				assertSentinel(t, err, test.err)

				return
			}

			if err != nil {
				t.Fatalf("ParseRepoRef(%q) returned an unexpected error: %v", test.raw, err)
			}

			if got != test.want {
				t.Errorf("ParseRepoRef(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}

func TestResolve_Sources(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		kind  config.SourceKind
		owner string
		repos []config.Repo
		err   error
	}{
		{
			name:  "org",
			yaml:  "groups:\n  a:\n    org: specsnl\n",
			kind:  config.SourceOrg,
			owner: "specsnl",
		},
		{
			name:  "user",
			yaml:  "groups:\n  a:\n    user: Ilyes512\n",
			kind:  config.SourceUser,
			owner: "Ilyes512",
		},
		{
			name:  "repos",
			yaml:  "groups:\n  a:\n    repos: [specsnl/labelsync, specsnl/specs-cli]\n",
			kind:  config.SourceRepos,
			repos: []config.Repo{{Owner: "specsnl", Name: "labelsync"}, {Owner: "specsnl", Name: "specs-cli"}},
		},
		{
			name: "org and user together are ambiguous",
			yaml: "groups:\n  a:\n    org: specsnl\n    user: Ilyes512\n",
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "org and repos together are ambiguous",
			yaml: "groups:\n  a:\n    org: specsnl\n    repos: [specsnl/labelsync]\n",
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a source plus include_groups is ambiguous",
			yaml: "groups:\n  a:\n    org: specsnl\n  b:\n    org: acme\n    include_groups: [a]\n",
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a group with no source at all",
			yaml: "groups:\n  a:\n",
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a group whose only fields are filters",
			yaml: "groups:\n  a:\n    include: [\"boilr-*\"]\n",
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a repos entry that is not owner/repo",
			yaml: "groups:\n  a:\n    repos: [labelsync]\n",
			err:  labelsync.ErrInvalidRepoRef,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resolve(t, test.yaml, "")

			if test.err != nil {
				assertSentinel(t, err, test.err)

				return
			}

			if err != nil {
				t.Fatalf("Resolve returned an unexpected error: %v", err)
			}

			selectors := res.SelectorsFor("a")
			if len(selectors) != 1 {
				t.Fatalf("SelectorsFor(a) returned %d selectors, want 1", len(selectors))
			}

			got := selectors[0]

			if got.Kind != test.kind {
				t.Errorf("kind = %q, want %q", got.Kind, test.kind)
			}

			if got.Owner != test.owner {
				t.Errorf("owner = %q, want %q", got.Owner, test.owner)
			}

			if !slices.Equal(got.Repos, test.repos) {
				t.Errorf("repos = %v, want %v", got.Repos, test.repos)
			}
		})
	}
}

// TestResolve_AmbiguousSourceNamesTheGroup pins that the message says which
// group is wrong and what it set: a config with a dozen groups is otherwise a
// search rather than a fix.
func TestResolve_AmbiguousSourceNamesTheGroup(t *testing.T) {
	_, err := resolve(t, "groups:\n  everything:\n    org: specsnl\n    user: Ilyes512\n", "")

	assertSentinel(t, err, labelsync.ErrAmbiguousGroupSource)

	for _, want := range []string{`"everything"`, "org", "user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestResolve_IncludeGroups(t *testing.T) {
	const yaml = `
groups:
  specs:
    org: specsnl
  personal:
    user: Ilyes512
  laravel:
    repos: [specsnl/example-website]
  everything:
    include_groups: [specs, personal]
  all-of-it:
    include_groups: [everything, laravel]
`

	res, err := resolve(t, yaml, "")
	if err != nil {
		t.Fatalf("Resolve returned an unexpected error: %v", err)
	}

	tests := []struct {
		group string
		want  []string
	}{
		{group: "specs", want: []string{"specs"}},
		{group: "everything", want: []string{"specs", "personal"}},
		{group: "all-of-it", want: []string{"specs", "personal", "laravel"}},
		{group: "undefined", want: nil},
	}

	for _, test := range tests {
		t.Run(test.group, func(t *testing.T) {
			if got := groupsOf(res.SelectorsFor(test.group)); !slices.Equal(got, test.want) {
				t.Errorf("SelectorsFor(%q) = %v, want %v", test.group, got, test.want)
			}
		})
	}
}

// TestResolve_Diamond pins that reaching the same group twice is composition,
// not a cycle, and that it contributes one selector rather than two.
func TestResolve_Diamond(t *testing.T) {
	const yaml = `
groups:
  leaf:
    org: specsnl
  left:
    include_groups: [leaf]
  right:
    include_groups: [leaf]
  top:
    include_groups: [left, right]
`

	res, err := resolve(t, yaml, "")
	if err != nil {
		t.Fatalf("Resolve returned an unexpected error: %v", err)
	}

	if got := groupsOf(res.SelectorsFor("top")); !slices.Equal(got, []string{"leaf"}) {
		t.Errorf("SelectorsFor(top) = %v, want [leaf]", got)
	}
}

func TestResolve_Cycles(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		path string
	}{
		{
			name: "a group including itself",
			yaml: "groups:\n  a:\n    include_groups: [a]\n",
			path: "a -> a",
		},
		{
			name: "two groups including each other",
			yaml: "groups:\n  a:\n    include_groups: [b]\n  b:\n    include_groups: [a]\n",
			path: "a -> b -> a",
		},
		{
			name: "a longer ring",
			yaml: "groups:\n  a:\n    include_groups: [b]\n  b:\n    include_groups: [c]\n  c:\n    include_groups: [a]\n",
			path: "a -> b -> c -> a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolve(t, test.yaml, "")

			assertSentinel(t, err, labelsync.ErrCyclicGroup)

			if !strings.Contains(err.Error(), test.path) {
				t.Errorf("error %q does not name the cycle %q", err, test.path)
			}
		})
	}
}

func TestResolve_IncludeGroupsUnknown(t *testing.T) {
	_, err := resolve(t, "groups:\n  a:\n    include_groups: [nope]\n", "")

	assertSentinel(t, err, labelsync.ErrUnknownGroup)
}

// TestValidateReportsTheResolveMessage pins the messages a user actually sees
// for the two group rules, because Validate is what reports them: it reaches
// them through Resolve rather than checking them itself, and the reason it does
// is that the second copy these once had drifted — printing the cycle chain with
// a different arrow than the docs promise. A change here is a documentation
// change too, in usage/_index.md and architecture/configuration.md.
func TestValidateReportsTheResolveMessage(t *testing.T) {
	const labels = "labels:\n  - name: bug\n    color: d73a4a\n"

	tests := []struct {
		name string
		yaml string
		want string
		err  error
	}{
		{
			name: "a cycle names the chain with ASCII arrows",
			yaml: "version: 1\ngroups:\n  a:\n    include_groups: [b]\n  b:\n    include_groups: [a]\n" + labels,
			want: "a -> b -> a",
			err:  labelsync.ErrCyclicGroup,
		},
		{
			name: "a group setting two sources names both",
			yaml: "version: 1\ngroups:\n  a:\n    org: specsnl\n    user: Ilyes512\n" + labels,
			want: `group "a" sets org and user`,
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a group setting no source says so",
			yaml: "version: 1\ngroups:\n  a:\n" + labels,
			want: `group "a" sets none of them`,
			err:  labelsync.ErrAmbiguousGroupSource,
		},
		{
			name: "a bad repository reference names the group",
			yaml: "version: 1\ngroups:\n  a:\n    repos: [\"specsnl/label sync\"]\n" + labels,
			want: `group "a": invalid repository reference`,
			err:  labelsync.ErrInvalidRepoRef,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(test.yaml))
			if err != nil {
				t.Fatalf("Parse returned an unexpected error: %v", err)
			}

			err = cfg.Validate()

			assertSentinel(t, err, test.err)

			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("Validate error %q does not contain %q", err, test.want)
			}
		})
	}
}

func TestSelector_Globs(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		repo string
		want bool
	}{
		{
			name: "no globs takes everything",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: "anything",
			want: true,
		},
		{
			name: "include is an allowlist",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"boilr-*\"]\n",
			repo: "boilr-go",
			want: true,
		},
		{
			name: "what include does not name is out",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"boilr-*\"]\n",
			repo: "labelsync",
			want: false,
		},
		{
			name: "exclude is applied after include",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"boilr-*\"]\n    exclude: [\"*-archive\"]\n",
			repo: "boilr-go-archive",
			want: false,
		},
		{
			name: "exclude alone is a denylist over everything",
			yaml: "groups:\n  a:\n    org: specsnl\n    exclude: [\"sandbox-*\"]\n",
			repo: "sandbox-one",
			want: false,
		},
		{
			name: "globs match the repository name only, never owner/repo",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"specsnl/*\"]\n",
			repo: "labelsync",
			want: false,
		},
		{
			name: "matching is case-insensitive, as GitHub's names are",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"BOILR-*\"]\n",
			repo: "boilr-go",
			want: true,
		},
		{
			name: "a character class",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"repo-[0-9]\"]\n",
			repo: "repo-7",
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resolve(t, test.yaml, "")
			if err != nil {
				t.Fatalf("Resolve returned an unexpected error: %v", err)
			}

			repo := config.Repo{Owner: "specsnl", Name: test.repo}

			if got := res.Matches("a", repo); got != test.want {
				t.Errorf("Matches(a, %s) = %t, want %t", repo, got, test.want)
			}
		})
	}
}

func TestSelector_Filters(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		repo config.Repo
		want bool
	}{
		{
			name: "skip_archived defaults to true",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "specsnl", Name: "old", Archived: true},
			want: false,
		},
		{
			name: "skip_archived: false keeps archived repositories",
			yaml: "groups:\n  a:\n    org: specsnl\n    skip_archived: false\n",
			repo: config.Repo{Owner: "specsnl", Name: "old", Archived: true},
			want: true,
		},
		{
			name: "skip_forks defaults to true",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "specsnl", Name: "forked", Fork: true},
			want: false,
		},
		{
			name: "skip_forks: false keeps forks",
			yaml: "groups:\n  a:\n    org: specsnl\n    skip_forks: false\n",
			repo: config.Repo{Owner: "specsnl", Name: "forked", Fork: true},
			want: true,
		},
		{
			name: "visibility defaults to all",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "specsnl", Name: "secret", Private: true},
			want: true,
		},
		{
			name: "visibility: public excludes private repositories",
			yaml: "groups:\n  a:\n    org: specsnl\n    visibility: public\n",
			repo: config.Repo{Owner: "specsnl", Name: "secret", Private: true},
			want: false,
		},
		{
			name: "visibility: private excludes public repositories",
			yaml: "groups:\n  a:\n    org: specsnl\n    visibility: private\n",
			repo: config.Repo{Owner: "specsnl", Name: "open"},
			want: false,
		},
		{
			name: "another owner is never in an org group",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "acme", Name: "labelsync"},
			want: false,
		},
		{
			name: "an owner matches case-insensitively",
			yaml: "groups:\n  a:\n    org: SpecsNL\n",
			repo: config.Repo{Owner: "specsnl", Name: "labelsync"},
			want: true,
		},
		{
			name: "an explicit repos entry carries no filters",
			yaml: "groups:\n  a:\n    repos: [specsnl/old]\n",
			repo: config.Repo{Owner: "specsnl", Name: "old", Archived: true, Fork: true, Private: true},
			want: true,
		},
		{
			name: "a repository the repos list does not name",
			yaml: "groups:\n  a:\n    repos: [specsnl/labelsync]\n",
			repo: config.Repo{Owner: "specsnl", Name: "other"},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resolve(t, test.yaml, "")
			if err != nil {
				t.Fatalf("Resolve returned an unexpected error: %v", err)
			}

			if got := res.Matches("a", test.repo); got != test.want {
				t.Errorf("Matches(a, %s) = %t, want %t", test.repo, got, test.want)
			}
		})
	}
}

// Reject is Matches with its reasoning, and the two are one function so that the
// reason a repository was filtered out cannot disagree with whether it was. This
// pins the halves against each other over the same table Matches is checked on,
// and pins each reason to the filter that produced it — `labelsync groups`
// prints them, and a reason naming the wrong filter sends a reader to the wrong
// line of their config.
func TestSelector_RejectExplainsEveryFilter(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		repo config.Repo
		want string
	}{
		{
			name: "selected",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "specsnl", Name: "labelsync"},
			want: "",
		},
		{
			name: "another owner",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "acme", Name: "labelsync"},
			want: "owner",
		},
		{
			name: "archived",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "specsnl", Name: "old", Archived: true},
			want: "skip_archived",
		},
		{
			name: "fork",
			yaml: "groups:\n  a:\n    org: specsnl\n",
			repo: config.Repo{Owner: "specsnl", Name: "forked", Fork: true},
			want: "skip_forks",
		},
		{
			name: "visibility",
			yaml: "groups:\n  a:\n    org: specsnl\n    visibility: public\n",
			repo: config.Repo{Owner: "specsnl", Name: "secret", Private: true},
			want: "visibility: public",
		},
		{
			name: "no include glob matched",
			yaml: "groups:\n  a:\n    org: specsnl\n    include: [\"boilr-*\"]\n",
			repo: config.Repo{Owner: "specsnl", Name: "labelsync"},
			want: "include",
		},
		{
			name: "an exclude glob matched",
			yaml: "groups:\n  a:\n    org: specsnl\n    exclude: [\"*-archive\"]\n",
			repo: config.Repo{Owner: "specsnl", Name: "old-archive"},
			want: "exclude",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resolve(t, test.yaml, "")
			if err != nil {
				t.Fatalf("Resolve returned an unexpected error: %v", err)
			}

			selectors := res.SelectorsFor("a")
			if len(selectors) != 1 {
				t.Fatalf("SelectorsFor(a) returned %d selectors, want 1", len(selectors))
			}

			got := selectors[0].Reject(test.repo)

			if test.want == "" && got != "" {
				t.Fatalf("Reject(%s) = %q, want it selected", test.repo, got)
			}

			if test.want != "" && !strings.Contains(got, test.want) {
				t.Errorf("Reject(%s) = %q, want it to mention %q", test.repo, got, test.want)
			}

			// The halves cannot disagree: a repository is rejected exactly when
			// there is a reason for it.
			if matched := selectors[0].Matches(test.repo); matched != (got == "") {
				t.Errorf("Matches(%s) = %t but Reject returned %q", test.repo, matched, got)
			}
		})
	}
}

// TestResolve_UserSplit pins the decision the two user endpoints turn on. The
// selector has to carry it: internal/github cannot re-derive it without asking
// who the token belongs to all over again.
func TestResolve_UserSplit(t *testing.T) {
	tests := []struct {
		name          string
		user          string
		authenticated string
		want          bool
	}{
		{name: "the authenticated user", user: "Ilyes512", authenticated: "Ilyes512", want: true},
		{name: "the authenticated user, spelled differently", user: "ilyes512", authenticated: "Ilyes512", want: true},
		{name: "somebody else", user: "octocat", authenticated: "Ilyes512", want: false},
		{name: "an unknown authenticated user", user: "Ilyes512", authenticated: "", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resolve(t, "groups:\n  a:\n    user: "+test.user+"\n", test.authenticated)
			if err != nil {
				t.Fatalf("Resolve returned an unexpected error: %v", err)
			}

			if got := res.SelectorsFor("a")[0].AuthenticatedUser; got != test.want {
				t.Errorf("AuthenticatedUser = %t, want %t", got, test.want)
			}
		})
	}
}

func TestResolve_PrivateVisibilityWarning(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		authenticated string
		want          bool
	}{
		{
			name:          "private for another user warns",
			yaml:          "groups:\n  a:\n    user: octocat\n    visibility: private\n",
			authenticated: "Ilyes512",
			want:          true,
		},
		{
			name:          "private for the authenticated user is fine",
			yaml:          "groups:\n  a:\n    user: Ilyes512\n    visibility: private\n",
			authenticated: "Ilyes512",
			want:          false,
		},
		{
			name:          "public for another user is fine",
			yaml:          "groups:\n  a:\n    user: octocat\n    visibility: public\n",
			authenticated: "Ilyes512",
			want:          false,
		},
		{
			name:          "private for an org is fine",
			yaml:          "groups:\n  a:\n    org: specsnl\n    visibility: private\n",
			authenticated: "Ilyes512",
			want:          false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			res, err := resolve(t, test.yaml, test.authenticated)
			if err != nil {
				t.Fatalf("Resolve returned an unexpected error: %v", err)
			}

			warnings := res.Warnings()

			if got := len(warnings) > 0; got != test.want {
				t.Fatalf("warnings = %v, want a warning: %t", warnings, test.want)
			}

			if test.want && warnings[0].Group != "a" {
				t.Errorf("warning group = %q, want %q", warnings[0].Group, "a")
			}
		})
	}
}

// TestResolution_Selectors pins the enumeration work list: one entry per group
// that names repositories, however many composed groups point at it.
func TestResolution_Selectors(t *testing.T) {
	const yaml = `
groups:
  specs:
    org: specsnl
  personal:
    user: Ilyes512
  everything:
    include_groups: [personal, specs]
  also-everything:
    include_groups: [specs, personal]
`

	res, err := resolve(t, yaml, "")
	if err != nil {
		t.Fatalf("Resolve returned an unexpected error: %v", err)
	}

	if got := groupsOf(res.Selectors()); !slices.Equal(got, []string{"personal", "specs"}) {
		t.Errorf("Selectors() = %v, want [personal specs]", got)
	}

	if got := res.Names(); !slices.Equal(got, []string{"also-everything", "everything", "personal", "specs"}) {
		t.Errorf("Names() = %v, want them sorted", got)
	}
}

// TestResolution_Desired is the resolution rule itself, including the case the
// whole tool's safety rests on: a repository no group selects wants nothing,
// which is not the same answer as a config that declares nothing.
func TestResolution_Desired(t *testing.T) {
	const yaml = `
groups:
  specs:
    org: specsnl
  personal:
    user: Ilyes512
defaults:
  groups: [specs]
labels:
  - name: "type: bug"
    color: d73a4a
  - name: "type: feature"
    color: "0e8a16"
    groups: [personal]
  - name: shared
    color: "000000"
    groups: [specs, personal]
`

	res, err := resolve(t, yaml, "")
	if err != nil {
		t.Fatalf("Resolve returned an unexpected error: %v", err)
	}

	tests := []struct {
		name   string
		repo   config.Repo
		groups []string
		want   []string
	}{
		{
			name:   "a repository in one group",
			repo:   config.Repo{Owner: "specsnl", Name: "labelsync"},
			groups: []string{"specs"},
			want:   []string{"type: bug", "shared"},
		},
		{
			name:   "a repository in the other group",
			repo:   config.Repo{Owner: "Ilyes512", Name: "boilr-go"},
			groups: []string{"personal"},
			want:   []string{"type: feature", "shared"},
		},
		{
			name: "a repository no group selects is never touched",
			repo: config.Repo{Owner: "acme", Name: "unrelated"},
		},
		{
			name: "an archived repository falls out of its group",
			repo: config.Repo{Owner: "specsnl", Name: "labelsync", Archived: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := res.Groups(test.repo); !slices.Equal(got, test.groups) {
				t.Errorf("Groups(%s) = %v, want %v", test.repo, got, test.groups)
			}

			var got []string
			for _, label := range res.Desired(test.repo) {
				got = append(got, label.Name)
			}

			if !slices.Equal(got, test.want) {
				t.Errorf("Desired(%s) = %v, want %v", test.repo, got, test.want)
			}
		})
	}
}

// TestResolution_DesiredIgnoresUnknownGroups pins the division of labour: a
// label naming a group nothing defines is validate.go's error to report, and
// here it simply selects nothing rather than crashing or matching everything.
func TestResolution_DesiredIgnoresUnknownGroups(t *testing.T) {
	const yaml = `
groups:
  specs:
    org: specsnl
labels:
  - name: orphan
    color: d73a4a
    groups: [nope]
`

	res, err := resolve(t, yaml, "")
	if err != nil {
		t.Fatalf("Resolve returned an unexpected error: %v", err)
	}

	if got := res.Desired(config.Repo{Owner: "specsnl", Name: "labelsync"}); len(got) != 0 {
		t.Errorf("Desired = %v, want nothing", got)
	}
}

// resolve parses a config fragment and resolves it in one step, so a table
// entry can stay a YAML string.
func resolve(t *testing.T, yaml, authenticatedUser string) (*config.Resolution, error) {
	t.Helper()

	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse returned an unexpected error: %v", err)
	}

	return cfg.Resolve(authenticatedUser)
}

// groupsOf reduces selectors to the groups they came from, which is what the
// composition tests are actually asserting about.
func groupsOf(selectors []config.Selector) []string {
	var out []string

	for _, selector := range selectors {
		out = append(out, selector.Group)
	}

	return out
}

// assertSentinel checks both halves of the wrapping rule: errors.Is still
// matches, and KindOf still names the sentinel for the JSON error_kind field.
func assertSentinel(t *testing.T, err, want error) {
	t.Helper()

	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want one wrapping %v", err, want)
	}

	if got := labelsync.KindOf(err); got != labelsync.KindOf(want) {
		t.Errorf("KindOf(err) = %q, want %q", got, labelsync.KindOf(want))
	}
}
