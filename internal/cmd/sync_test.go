package cmd_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/exit"
)

// syncConfig targets two repositories by name, so a test decides what drifts by
// choosing what the fake API returns rather than by arranging an enumeration.
const syncConfig = `
version: 1

groups:
  all:
    repos:
      - specsnl/example-website
      - specsnl/example-platform

defaults:
  groups: [all]

labels:
  - name: "type: bug"
    color: "d73a4a"
    description: "Something isn't working"
  - name: "type: feature"
    color: "a2eeef"
    description: "New functionality"
`

// inSync is what both repositories hold when nothing has drifted: exactly the
// configured labels, with the configured colours and descriptions.
const inSync = `[
  {"name":"type: bug","color":"d73a4a","description":"Something isn't working"},
  {"name":"type: feature","color":"a2eeef","description":"New functionality"}
]`

// labelServer answers every label listing with body, and 404s anything else so a
// stray request shows up as a failure rather than as an empty label set.
func labelServer(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/labels") {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, `{"message":"Not Found"}`)

			return
		}

		writeJSON(w, body)
	})
}

// TestSync_ExitCodes is the point of the whole command. Without a code that
// means "succeeded, and found work", a CI dry-run can only ever pass, which
// makes it useless as a check — and the outcome codes are bits, so a run that
// both drifts and skips has to report both.
func TestSync_ExitCodes(t *testing.T) {
	unreachable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "example-platform") {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, `{"message":"Forbidden"}`)

			return
		}

		if strings.Contains(r.URL.Path, "example-website") {
			writeJSON(w, inSync)

			return
		}

		w.WriteHeader(http.StatusNotFound)
	})

	driftedAndUnreachable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "example-platform") {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, `{"message":"Forbidden"}`)

			return
		}

		writeJSON(w, `[]`)
	})

	for _, tc := range []struct {
		name    string
		handler http.Handler
		want    exit.Code
	}{
		{"in sync", labelServer(inSync), exit.OK},
		{"drift", labelServer(`[]`), exit.Drift},
		{"a repository could not be reached", unreachable, exit.Skipped},
		{"both at once", driftedAndUnreachable, exit.Drift | exit.Skipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, flags := fakeGitHub(t, tc.handler)
			config := writeConfig(t, syncConfig)

			_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run")...)

			if code := exit.Of(err); code != tc.want {
				t.Fatalf("exit code = %s, want %s (error: %v)", code, tc.want, err)
			}
		})
	}
}

// Exit 6 is worth spelling out on its own: it is the combination the bit scheme
// exists for, and ranking the two outcomes instead would throw half the answer
// away.
func TestSync_DriftAndSkipCombineToSix(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "example-platform") {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, `{"message":"Forbidden"}`)

			return
		}

		writeJSON(w, `[]`)
	})

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, syncConfig)

	_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run")...)

	if code := exit.Of(err); code != 6 {
		t.Fatalf("exit code = %s, want 6", code)
	}
}

// A silent carrier that still prints is the failure mode exit.Err exists to
// prevent: the drift *was* the successful result, and the diff is already on
// stdout.
func TestSync_DriftPrintsNothingAtErrorLevel(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(`[]`))
	config := writeConfig(t, syncConfig)

	_, stdout, stderr, err := runApp(t, app, nil, args(config, flags, "--output", "json", "sync", "--dry-run")...)
	if code := exit.Of(err); code != exit.Drift {
		t.Fatalf("exit code = %s, want %s", code, exit.Drift)
	}

	var carrier *exit.Err
	if !errors.As(err, &carrier) || carrier.Err != nil {
		t.Fatalf("carrier is not silent: %#v", err)
	}

	for _, line := range jsonLines(t, stderr) {
		if line["level"] == "error" {
			t.Errorf("stderr carries an error-level line for a drifting dry run: %v", line)
		}
	}

	// The diff is the product, and it is what a pipeline reads.
	if !strings.Contains(stdout, `"kind":"create"`) {
		t.Errorf("stdout = %q, want the planned creates", stdout)
	}
}

// A dry run writes nothing. The fake API refuses anything that is not a listing,
// so a stray write would surface as a failure — this asserts it directly anyway,
// because "it did not write" is the promise the flag makes.
func TestSync_DryRunWritesNothing(t *testing.T) {
	handler, log := watch(labelServer(`[]`))

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, syncConfig)

	runApp(t, app, nil, args(config, flags, "sync", "--dry-run")...) //nolint:errcheck // The exit code is asserted elsewhere.

	for _, request := range log.all() {
		if !strings.HasPrefix(request, http.MethodGet+" ") {
			t.Errorf("a write was issued during a dry run: %s", request)
		}
	}
}

// --repo bypasses enumeration, not the config: the repository still gets the
// labels its groups ask for.
func TestSync_RepoFlagBypassesEnumeration(t *testing.T) {
	handler, log := watch(labelServer(inSync))

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, syncConfig)

	_, stdout, _, err := runApp(t, app, nil,
		args(config, flags, "sync", "--dry-run", "--repo", "specsnl/example-website")...)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if requests := log.all(); len(requests) != 1 || !strings.Contains(requests[0], "example-website") {
		t.Fatalf("requests = %v, want only the named repository", requests)
	}

	if !strings.Contains(stdout, "specsnl/example-website") {
		t.Errorf("stdout = %q, want the named repository in the diff", stdout)
	}
}

// A repository no configured label selects is never touched, and one named
// outright says so rather than being silently dropped.
func TestSync_RepoNoGroupSelectsIsNotTouched(t *testing.T) {
	handler, log := watch(labelServer(`[]`))

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, syncConfig)

	_, _, stderr, err := runApp(t, app, nil,
		args(config, flags, "sync", "--dry-run", "--repo", "acme/unrelated")...)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if requests := log.all(); len(requests) != 0 {
		t.Errorf("requests were issued for a repository no group selects: %v", requests)
	}

	if !strings.Contains(stderr, "acme/unrelated") {
		t.Errorf("stderr = %q, want it to say the repository will not be touched", stderr)
	}
}

// A bad --repo is the same rule a repos: entry is held to, and it fails before
// anything is enumerated.
func TestSync_RejectsABadRepoReference(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(inSync))
	config := writeConfig(t, syncConfig)

	_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run", "--repo", "labelsync")...)
	if !errors.Is(err, labelsync.ErrInvalidRepoRef) {
		t.Fatalf("error = %v, want one wrapping ErrInvalidRepoRef", err)
	}
}

// --group restricts the run, and a --group that names nothing is a typo rather
// than an empty plan.
func TestSync_GroupFlag(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(inSync))
	config := writeConfig(t, syncConfig)

	if _, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run", "--group", "all")...); err != nil {
		t.Fatalf("sync --group all: %v", err)
	}

	_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run", "--group", "nope")...)
	if !errors.Is(err, labelsync.ErrUnknownGroup) {
		t.Fatalf("error = %v, want one wrapping ErrUnknownGroup", err)
	}
}

// An invalid config fails the run, exits 1, and carries the error_kind that says
// which rule broke — the whole reason JSON output has the field.
func TestSync_InvalidConfigExitsOneWithAKind(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(inSync))
	config := writeConfig(t, `
version: 1

groups:
  all:
    repos: [specsnl/labelsync]

defaults:
  groups: [all]

labels:
  - name: "type: bug"
    color: "d73a4a"
  - name: "type: feature"
    color: "d73a4a"
`)

	_, _, stderr, err := runApp(t, app, nil, args(config, flags, "--output", "json", "sync", "--dry-run")...)
	if code := exit.Of(err); code != exit.Error {
		t.Fatalf("exit code = %s, want %s (error: %v)", code, exit.Error, err)
	}

	if kind := labelsync.KindOf(err); kind != "duplicate_label_color" {
		t.Errorf("error_kind = %q, want %q", kind, "duplicate_label_color")
	}

	// main is what prints the final line, so the stream itself is only asserted
	// to be free of a diff — a failed run has no live state to report on.
	if strings.Contains(stderr, `"kind":"create"`) {
		t.Errorf("stderr carries a plan for a run that never established one: %q", stderr)
	}
}

// --mode is parsed here even though prune execution is not landed, so a config
// written against prune fails on the flag rather than partway through a run.
func TestSync_ModeFlag(t *testing.T) {
	for _, tc := range []struct {
		mode string
		ok   bool
	}{
		{"append", true},
		{"prune", true},
		{"destroy", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			app, flags := fakeGitHub(t, labelServer(inSync))
			config := writeConfig(t, syncConfig)

			_, _, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run", "--mode", tc.mode)...)

			if tc.ok && err != nil {
				t.Fatalf("sync --mode %s: %v", tc.mode, err)
			}

			if !tc.ok && err == nil {
				t.Fatal("want an error for an unknown mode, got none")
			}
		})
	}
}

// prune mode reaches the planner, which records every unconfigured label as a
// removal candidate. Nothing is deleted — a dry run writes nothing — but the
// candidates are what the diff is for.
func TestSync_PruneModePlansDeletions(t *testing.T) {
	handler := labelServer(`[
	  {"name":"type: bug","color":"d73a4a","description":"Something isn't working"},
	  {"name":"type: feature","color":"a2eeef","description":"New functionality"},
	  {"name":"wontfix","color":"111111","description":"Old"}
	]`)

	app, flags := fakeGitHub(t, handler)
	config := writeConfig(t, syncConfig)

	_, stdout, _, err := runApp(t, app, nil,
		args(config, flags, "--output", "json", "sync", "--dry-run", "--mode", "prune")...)
	if code := exit.Of(err); code != exit.Drift {
		t.Fatalf("exit code = %s, want %s", code, exit.Drift)
	}

	if !strings.Contains(stdout, `"kind":"delete"`) {
		t.Errorf("stdout = %q, want the removal candidates", stdout)
	}
}

// Applying is not landed, and a sync that printed a plan and quietly applied
// nothing is the one outcome a user could not detect.
func TestSync_RefusesToApply(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(inSync))
	config := writeConfig(t, syncConfig)

	_, _, _, err := runApp(t, app, nil, args(config, flags, "sync")...)
	if err == nil {
		t.Fatal("want an error without --dry-run, got none")
	}

	if !strings.Contains(err.Error(), "--dry-run") {
		t.Errorf("error = %q, want it to point at --dry-run", err)
	}
}

// The pretty rendering is the diff a human reads, grouped per repository and
// closed by the summary line.
func TestSync_PrettyRendering(t *testing.T) {
	app, flags := fakeGitHub(t, labelServer(`[]`))
	config := writeConfig(t, syncConfig)

	_, stdout, _, err := runApp(t, app, nil, args(config, flags, "sync", "--dry-run")...)
	if exit.Of(err) != exit.Drift {
		t.Fatalf("exit code = %s, want %s (error: %v)", exit.Of(err), exit.Drift, err)
	}

	for _, want := range []string{
		"specsnl/example-website",
		"create",
		"type: bug",
		"2 repositories · 4 created",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

// The startup budget check is free and diagnostic, so it only happens under
// --debug — a run that spent a round trip on something nobody reads would be
// paying for nothing.
func TestSync_RateLimitCheckOnlyUnderDebug(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"without --debug", nil, false},
		{"with --debug", []string{"--debug"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, log := watch(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rate_limit" {
					writeJSON(w, `{"resources":{"core":{"limit":5000,"remaining":4999,"reset":4102444800}}}`)

					return
				}

				writeJSON(w, inSync)
			}))

			app, flags := fakeGitHub(t, handler)
			config := writeConfig(t, syncConfig)

			if _, _, _, err := runApp(t, app, nil, args(config, append(flags, tc.args...), "sync", "--dry-run")...); err != nil {
				t.Fatalf("sync: %v", err)
			}

			if asked := len(log.matching("/rate_limit")) > 0; asked != tc.want {
				t.Errorf("GET /rate_limit issued = %t, want %t", asked, tc.want)
			}
		})
	}
}
