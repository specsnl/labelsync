package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// stubEnv builds a LookupEnv over a map, so a test states the environment it
// wants rather than mutating the process's own.
func stubEnv(vars map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := vars[key]

		return value, ok
	}
}

// noEnv is an environment with nothing in it. Every test passes one of these
// explicitly: a Resolver with LookupEnv nil reads the real environment, and a
// developer with GH_TOKEN exported would otherwise see different results from CI.
func noEnv() func(string) (string, bool) { return stubEnv(nil) }

// stubConfig builds a ConfigToken returning a fixed token.
func stubConfig(token string) func(string) (string, string) {
	return func(string) (string, string) { return token, "oauth_token" }
}

// stubCLI builds a CLIToken returning a fixed token.
func stubCLI(token string) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return token, nil }
}

// failCLI builds a CLIToken that fails the way an absent gh does.
func failCLI(err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return "", err }
}

// TestResolvePrecedence is the property the chain exists to have: with several
// sources populated, the earliest one wins, and the winner is reported as the
// source it actually came from.
//
// Every case fills in the sources *below* the expected winner too, so a passing
// case proves the order rather than merely proving that the one populated source
// was found.
func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		resolver   Resolver
		wantValue  string
		wantSource TokenSource
	}{
		{
			name: "flag beats everything",
			resolver: Resolver{
				Flag:        "from-flag",
				LookupEnv:   stubEnv(map[string]string{"GH_TOKEN": "from-gh-token", "GITHUB_TOKEN": "from-github-token"}),
				ConfigToken: stubConfig("from-config"),
				CLIToken:    stubCLI("from-cli"),
			},
			wantValue:  "from-flag",
			wantSource: TokenSourceFlag,
		},
		{
			name: "GH_TOKEN beats GITHUB_TOKEN",
			resolver: Resolver{
				LookupEnv:   stubEnv(map[string]string{"GH_TOKEN": "from-gh-token", "GITHUB_TOKEN": "from-github-token"}),
				ConfigToken: stubConfig("from-config"),
				CLIToken:    stubCLI("from-cli"),
			},
			wantValue:  "from-gh-token",
			wantSource: TokenSourceGHToken,
		},
		{
			name: "GITHUB_TOKEN beats the gh config",
			resolver: Resolver{
				LookupEnv:   stubEnv(map[string]string{"GITHUB_TOKEN": "from-github-token"}),
				ConfigToken: stubConfig("from-config"),
				CLIToken:    stubCLI("from-cli"),
			},
			wantValue:  "from-github-token",
			wantSource: TokenSourceGitHubToken,
		},
		{
			name: "gh config beats the gh shell-out",
			resolver: Resolver{
				LookupEnv:   noEnv(),
				ConfigToken: stubConfig("from-config"),
				CLIToken:    stubCLI("from-cli"),
			},
			wantValue:  "from-config",
			wantSource: TokenSourceGHConfig,
		},
		{
			name: "gh shell-out is the last resort",
			resolver: Resolver{
				LookupEnv:   noEnv(),
				ConfigToken: stubConfig(""),
				CLIToken:    stubCLI("from-cli"),
			},
			wantValue:  "from-cli",
			wantSource: TokenSourceGHCLI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := tt.resolver.Resolve(t.Context())
			if err != nil {
				t.Fatalf("Resolve() error = %v, want nil", err)
			}

			if token.Value != tt.wantValue {
				t.Errorf("token value = %q, want %q", token.Value, tt.wantValue)
			}

			if token.Source != tt.wantSource {
				t.Errorf("token source = %q, want %q", token.Source, tt.wantSource)
			}
		})
	}
}

// TestResolveSkipsEmptySources covers the cases where a source is present but
// carries nothing: a variable exported as the empty string, a variable set to
// whitespace, and `gh auth token` returning its trailing newline and no more.
// All of them are the same absence, and the chain must walk past them rather
// than resolve to a token that will fail on the first request.
func TestResolveSkipsEmptySources(t *testing.T) {
	tests := []struct {
		name       string
		resolver   Resolver
		wantValue  string
		wantSource TokenSource
	}{
		{
			name: "flag set to empty falls through",
			resolver: Resolver{
				Flag:        "",
				LookupEnv:   stubEnv(map[string]string{"GH_TOKEN": "from-gh-token"}),
				ConfigToken: stubConfig(""),
				CLIToken:    stubCLI(""),
			},
			wantValue:  "from-gh-token",
			wantSource: TokenSourceGHToken,
		},
		{
			name: "variable exported empty falls through",
			resolver: Resolver{
				LookupEnv:   stubEnv(map[string]string{"GH_TOKEN": "", "GITHUB_TOKEN": "from-github-token"}),
				ConfigToken: stubConfig(""),
				CLIToken:    stubCLI(""),
			},
			wantValue:  "from-github-token",
			wantSource: TokenSourceGitHubToken,
		},
		{
			name: "variable set to whitespace falls through",
			resolver: Resolver{
				LookupEnv:   stubEnv(map[string]string{"GH_TOKEN": "   ", "GITHUB_TOKEN": "\t\n"}),
				ConfigToken: stubConfig("from-config"),
				CLIToken:    stubCLI(""),
			},
			wantValue:  "from-config",
			wantSource: TokenSourceGHConfig,
		},
		{
			name: "surrounding whitespace is trimmed off the winner",
			resolver: Resolver{
				LookupEnv:   noEnv(),
				ConfigToken: stubConfig(""),
				CLIToken:    stubCLI("gho_fromcli\n"),
			},
			wantValue:  "gho_fromcli",
			wantSource: TokenSourceGHCLI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := tt.resolver.Resolve(t.Context())
			if err != nil {
				t.Fatalf("Resolve() error = %v, want nil", err)
			}

			if token.Value != tt.wantValue {
				t.Errorf("token value = %q, want %q", token.Value, tt.wantValue)
			}

			if token.Source != tt.wantSource {
				t.Errorf("token source = %q, want %q", token.Source, tt.wantSource)
			}
		})
	}
}

// TestResolveNoToken covers the exhausted chain. The error must wrap the
// sentinel — an error that only reads right but does not match errors.Is renders
// an empty error_kind in JSON output — and its message must name all four
// sources, because a user who has just been told "no GitHub token found" needs
// to be told what would count as one.
func TestResolveNoToken(t *testing.T) {
	resolver := Resolver{
		LookupEnv:   noEnv(),
		ConfigToken: stubConfig(""),
		CLIToken:    stubCLI(""),
	}

	token, err := resolver.Resolve(t.Context())
	if !errors.Is(err, labelsync.ErrNoToken) {
		t.Fatalf("Resolve() error = %v, want one wrapping ErrNoToken", err)
	}

	if labelsync.KindOf(err) != "no_token" {
		t.Errorf("KindOf(err) = %q, want %q", labelsync.KindOf(err), "no_token")
	}

	if token.Value != "" {
		t.Errorf("token value = %q, want empty on failure", token.Value)
	}

	for _, want := range []string{"--token", "GH_TOKEN", "GITHUB_TOKEN", "gh auth login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q does not name %q", err.Error(), want)
		}
	}
}

// TestResolveFailingSourceIsNotFatal pins the decision that a broken step is not
// a broken run. gh not being installed is the ordinary case on a CI runner, and
// it must produce ErrNoToken naming the alternatives rather than an exec error
// nobody can act on.
func TestResolveFailingSourceIsNotFatal(t *testing.T) {
	resolver := Resolver{
		LookupEnv:   noEnv(),
		ConfigToken: stubConfig(""),
		CLIToken:    failCLI(exec.ErrNotFound),
	}

	_, err := resolver.Resolve(t.Context())
	if !errors.Is(err, labelsync.ErrNoToken) {
		t.Fatalf("Resolve() error = %v, want one wrapping ErrNoToken", err)
	}

	if errors.Is(err, exec.ErrNotFound) {
		t.Error("Resolve() surfaced the exec failure; a failing step should be logged and skipped")
	}
}

// TestResolveDebugNamesWinningSource covers the reporting half of the feature:
// under --debug the run says which of the four sources it authenticated with,
// because "it used a different token than I thought" is the failure this chain
// makes possible and the log line is the only way to see it.
func TestResolveDebugNamesWinningSource(t *testing.T) {
	tests := []struct {
		name     string
		resolver Resolver
		want     string
	}{
		{
			name:     "flag",
			resolver: Resolver{Flag: "from-flag", LookupEnv: noEnv(), ConfigToken: stubConfig(""), CLIToken: stubCLI("")},
			want:     string(TokenSourceFlag),
		},
		{
			name:     "environment",
			resolver: Resolver{LookupEnv: stubEnv(map[string]string{"GH_TOKEN": "t"}), ConfigToken: stubConfig(""), CLIToken: stubCLI("")},
			want:     string(TokenSourceGHToken),
		},
		{
			name:     "gh config",
			resolver: Resolver{LookupEnv: noEnv(), ConfigToken: stubConfig("t"), CLIToken: stubCLI("")},
			want:     string(TokenSourceGHConfig),
		},
		{
			name:     "gh shell-out",
			resolver: Resolver{LookupEnv: noEnv(), ConfigToken: stubConfig(""), CLIToken: stubCLI("t")},
			want:     string(TokenSourceGHCLI),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logged := captureDebug(t)

			if _, err := tt.resolver.Resolve(t.Context()); err != nil {
				t.Fatalf("Resolve() error = %v, want nil", err)
			}

			got := logged.String()
			if !strings.Contains(got, "token resolved") {
				t.Fatalf("debug output %q does not report a resolved token", got)
			}

			if !strings.Contains(got, tt.want) {
				t.Errorf("debug output %q does not name the winning source %q", got, tt.want)
			}
		})
	}
}

// TestResolveDebugNeverLogsTheToken is the other half of the redaction promise
// on [Token]: the source is diagnostic, the value never is. A credential written
// to a CI log is not recoverable by editing the log.
func TestResolveDebugNeverLogsTheToken(t *testing.T) {
	const secret = "gho_supersecretvalue"

	logged := captureDebug(t)

	resolver := Resolver{
		LookupEnv:   stubEnv(map[string]string{"GH_TOKEN": secret}),
		ConfigToken: stubConfig(""),
		CLIToken:    stubCLI(""),
	}

	token, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}

	if token.Value != secret {
		t.Fatalf("token value = %q, want %q", token.Value, secret)
	}

	if strings.Contains(logged.String(), secret) {
		t.Errorf("debug output %q contains the token", logged.String())
	}
}

// TestTokenRedaction covers the two routes a struct usually takes to an output
// stream. Both have to redact, because the type is the only place the guarantee
// can live — a call site that gets it right today is not a guarantee about the
// call site added tomorrow.
func TestTokenRedaction(t *testing.T) {
	const secret = "gho_supersecretvalue"

	token := Token{Value: secret, Source: TokenSourceGHToken}

	t.Run("fmt", func(t *testing.T) {
		for _, format := range []string{"%v", "%s", "%+v"} {
			got := fmt.Sprintf(format, token)
			if strings.Contains(got, secret) {
				t.Errorf("fmt.Sprintf(%q, token) = %q, which contains the token", format, got)
			}

			if !strings.Contains(got, string(TokenSourceGHToken)) {
				t.Errorf("fmt.Sprintf(%q, token) = %q, which does not name the source", format, got)
			}
		}
	})

	t.Run("slog", func(t *testing.T) {
		logged := captureDebug(t)

		slog.Debug("resolved", "token", token)

		got := logged.String()
		if strings.Contains(got, secret) {
			t.Errorf("slog output %q contains the token", got)
		}

		if !strings.Contains(got, string(TokenSourceGHToken)) {
			t.Errorf("slog output %q does not name the source", got)
		}
	})
}

// TestGHAuthToken covers the real shell-out rather than the seam the other
// tests inject over — the arguments it passes, the trailing newline gh emits,
// and the fact that gh's own complaint on stderr reaches the debug log instead
// of a bare "exit status 1".
//
// The fake gh is a script on a PATH built for the test. Nothing here runs the
// real binary, so the test says the same thing on a laptop with a gh login and
// on a CI runner without one.
func TestGHAuthToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}

	tests := []struct {
		name    string
		script  string
		want    string
		wantErr string
	}{
		{
			name:   "asks gh for a token for the right host",
			script: "#!/bin/sh\necho \"$*\"\n",
			want:   "auth token --hostname " + Host + "\n",
		},
		{
			name:   "output is returned raw, for Resolve to trim",
			script: "#!/bin/sh\necho gho_fromcli\n",
			want:   "gho_fromcli\n",
		},
		{
			name:    "stderr reaches the error",
			script:  "#!/bin/sh\necho 'not logged into any GitHub hosts' >&2\nexit 1\n",
			wantErr: "not logged into any GitHub hosts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", fakeGH(t, tt.script))

			got, err := ghAuthToken(t.Context(), Host)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ghAuthToken() error = nil, want one containing %q", tt.wantErr)
				}

				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ghAuthToken() error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("ghAuthToken() error = %v, want nil", err)
			}

			if got != tt.want {
				t.Errorf("ghAuthToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeGH writes script to a directory as an executable named gh and returns that
// directory as the whole of PATH, so the only gh reachable is this one.
func fakeGH(t *testing.T, script string) string {
	t.Helper()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake gh: %v", err)
	}

	return dir
}

// captureDebug points the default slog logger at a buffer for the duration of
// the test and restores the previous one afterwards. The package's tests do not
// run in parallel, so swapping a process-wide default is safe here.
func captureDebug(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	previous := slog.Default()

	t.Cleanup(func() { slog.SetDefault(previous) })

	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	return &buf
}
