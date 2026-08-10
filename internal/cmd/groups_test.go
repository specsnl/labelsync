package cmd_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
)

// groupsConfig exercises every source kind at once: an org whose filters have
// something to remove, an explicit repos list, a composed group over both, and a
// group whose org selects nothing at all.
const groupsConfig = `
version: 1

groups:
  websites:
    org: specsnl
    exclude: ["*-archive"]
  platform:
    repos:
      - specsnl/example-platform
  everything:
    include_groups: [websites, platform]
  empty:
    org: nobody

defaults:
  groups: [websites]

labels:
  - name: "type: bug"
    color: "d73a4a"
    description: "Something isn't working"
`

// orgListing answers the two org listings groupsConfig asks for: one with four
// repositories, three of which the filters remove, and one with none.
func orgListing() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/orgs/specsnl/") {
			writeJSON(w, `[]`)

			return
		}

		writeJSON(w, "["+strings.Join([]string{
			repoJSON("specsnl", "example-website", false, false, false, true),
			repoJSON("specsnl", "old-archive", false, false, false, true),
			repoJSON("specsnl", "retired", true, false, false, true),
			repoJSON("specsnl", "a-fork", false, true, false, true),
		}, ",")+"]")
	})
}

// TestGroups_ResolvesMembership is the command's whole product: every group,
// what each one selects, and the count a consumer filters on.
func TestGroups_ResolvesMembership(t *testing.T) {
	app, flags := fakeGitHub(t, orgListing())
	config := writeConfig(t, groupsConfig)

	_, stdout, _, err := runApp(t, app, nil, args(config, flags, "--output", "json", "groups")...)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}

	rows := make(map[string]map[string]any)

	for _, row := range jsonLines(t, stdout) {
		name, _ := row["group"].(string)
		rows[name] = row
	}

	for _, tc := range []struct {
		group string
		count float64
		repos []string
	}{
		{"websites", 1, []string{"specsnl/example-website"}},
		{"platform", 1, []string{"specsnl/example-platform"}},
		{"everything", 2, []string{"specsnl/example-platform", "specsnl/example-website"}},
		{"empty", 0, nil},
	} {
		t.Run(tc.group, func(t *testing.T) {
			row, ok := rows[tc.group]
			if !ok {
				t.Fatalf("no row for group %q; got %v", tc.group, rows)
			}

			// A number, not a string: the whole reason the row is a struct with
			// json tags rather than a table of cells.
			if got := row["repositories"]; got != tc.count {
				t.Errorf("repositories = %v (%T), want %v", got, got, tc.count)
			}

			var got []string

			for _, repo := range row["repos"].([]any) {
				got = append(got, repo.(string))
			}

			if strings.Join(got, ",") != strings.Join(tc.repos, ",") {
				t.Errorf("repos = %v, want %v", got, tc.repos)
			}
		})
	}
}

// The absence of an expected repository is what this command exists to explain,
// so every filtered-out repository says which filter removed it.
func TestGroups_ExplainsWhatWasFilteredOut(t *testing.T) {
	app, flags := fakeGitHub(t, orgListing())
	config := writeConfig(t, groupsConfig)

	_, _, stderr, err := runApp(t, app, nil, args(config, flags, "groups", "--group", "websites")...)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}

	for repo, reason := range map[string]string{
		"specsnl/old-archive": "exclude glob",
		"specsnl/retired":     "archived",
		"specsnl/a-fork":      "fork",
	} {
		if !strings.Contains(stderr, repo) {
			t.Errorf("stderr does not mention %s: %q", repo, stderr)
		}

		if !strings.Contains(stderr, reason) {
			t.Errorf("stderr does not say %q for %s: %q", reason, repo, stderr)
		}
	}
}

// A group that selects nothing is the thing a maintainer is here to find out,
// and it is reported rather than left to be inferred from a zero in a table.
func TestGroups_FlagsEmptyGroups(t *testing.T) {
	app, flags := fakeGitHub(t, orgListing())
	config := writeConfig(t, groupsConfig)

	_, _, stderr, err := runApp(t, app, nil, args(config, flags, "groups", "--group", "empty")...)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}

	if !strings.Contains(stderr, `group "empty" resolves to no repositories`) {
		t.Errorf("stderr = %q, want it to flag the empty group", stderr)
	}
}

// visibility: private for anybody but the token's own user selects nothing, and
// says so — the config is well-formed, so this is a warning rather than an error.
func TestGroups_WarnsAboutPrivateVisibilityForAnotherUser(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			writeJSON(w, `{"login":"somebody-else"}`)

			return
		}

		writeJSON(w, `[]`)
	})

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, `
version: 1

groups:
  theirs:
    user: Ilyes512
    visibility: private

labels:
  - name: "type: bug"
    color: "d73a4a"
    groups: [theirs]
`)

	_, _, stderr, err := runApp(t, app, nil, args(config, flags, "groups")...)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}

	if !strings.Contains(stderr, "visibility: private") {
		t.Errorf("stderr = %q, want the private-visibility warning", stderr)
	}
}

// A --group naming a group the config does not define is a typo, and reporting
// an empty table for it is how a maintainer concludes a working selector is
// broken.
func TestGroups_RejectsAnUnknownGroup(t *testing.T) {
	app, flags := fakeGitHub(t, orgListing())
	config := writeConfig(t, groupsConfig)

	_, _, _, err := runApp(t, app, nil, args(config, flags, "groups", "--group", "webistes")...)
	if !errors.Is(err, labelsync.ErrUnknownGroup) {
		t.Fatalf("error = %v, want one wrapping ErrUnknownGroup", err)
	}

	if kind := labelsync.KindOf(err); kind != "unknown_group" {
		t.Errorf("error_kind = %q, want %q", kind, "unknown_group")
	}
}

// An owner that cannot be listed does not take the other groups down with it: it
// is collected, reported, and turns into the skipped outcome bit.
func TestGroups_SkippedOwnerExitsFour(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/orgs/nobody/") {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, `{"message":"Not Found"}`)

			return
		}

		orgListing().ServeHTTP(w, r)
	})

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, groupsConfig)

	_, stdout, stderr, err := runApp(t, app, nil, args(config, flags, "groups")...)
	if code := exit.Of(err); code != exit.Skipped {
		t.Fatalf("exit code = %s, want %s (error: %v)", code, exit.Skipped, err)
	}

	// The skip is an outcome, not a failure: the table it did produce is still
	// the command's answer.
	if !strings.Contains(stdout, "specsnl/example-website") {
		t.Errorf("stdout = %q, want the groups that did resolve", stdout)
	}

	if !strings.Contains(stderr, "skipped") {
		t.Errorf("stderr = %q, want the skipped-repository summary", stderr)
	}
}

// The pretty rendering is a table, and an empty group's cell says so rather than
// being blank — a blank cell reads as "nothing to report".
func TestGroups_PrettyRendering(t *testing.T) {
	app, flags := fakeGitHub(t, orgListing())
	config := writeConfig(t, groupsConfig)

	_, stdout, _, err := runApp(t, app, nil, args(config, flags, "groups")...)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}

	for _, want := range []string{"Group", "Source", "org: specsnl", "specsnl/example-website", "none"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

// A config with no user: group must not spend a request asking who the token
// belongs to — the answer decides nothing for it.
func TestGroups_DoesNotResolveTheLoginWithoutAUserGroup(t *testing.T) {
	var asked bool

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			asked = true
		}

		orgListing().ServeHTTP(w, r)
	})

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, groupsConfig)

	if _, _, _, err := runApp(t, app, nil, args(config, flags, "groups")...); err != nil {
		t.Fatalf("groups: %v", err)
	}

	if asked {
		t.Error("GET /user was issued for a config with no user: group")
	}
}
