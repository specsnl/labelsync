// Package github is labelsync's GitHub boundary. Everything that speaks HTTP to
// the API lives here, and nothing outside it does: internal/plan and
// internal/palette take plain structs and return plain structs precisely so that
// the interesting logic stays testable without any of this.
//
// auth.go resolves the credential the rest of the package authenticates with.
//
// # The resolution chain
//
// A token is resolved from four sources, in a fixed order, first non-empty wins:
//
//  1. the --token flag
//  2. GH_TOKEN, then GITHUB_TOKEN
//  3. the gh config file, via go-gh's auth.TokenForHost
//  4. shelling out to `gh auth token`
//
// go-gh is a dependency for this and nothing else. Every other request in this
// package goes through go-github.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// Host is the only GitHub host labelsync talks to. GitHub Enterprise Server is
// a non-goal, so the host is a constant rather than a flag — but it is named
// rather than inlined, because both the gh config lookup and the `gh auth token`
// shell-out have to agree on which host they are asking about.
const Host = "github.com"

// noTokenHelp names every source the chain tried, so a failed run tells the user
// all four ways to fix it rather than only the one they were most likely
// attempting.
const noTokenHelp = "pass --token, set GH_TOKEN or GITHUB_TOKEN, " +
	"or log in with `gh auth login` (read from the gh config, then from `gh auth token`)"

// TokenSource names the step of the resolution chain that produced a token. The
// values are what --debug prints, so they read as the thing the user would go
// and change.
type TokenSource string

// The four steps of the chain. GH_TOKEN and GITHUB_TOKEN are separate sources
// because they are separate variables: reporting "an environment variable won"
// would leave a user with both set no better off than before.
const (
	TokenSourceFlag        TokenSource = "--token"
	TokenSourceGHToken     TokenSource = "GH_TOKEN"
	TokenSourceGitHubToken TokenSource = "GITHUB_TOKEN"
	TokenSourceGHConfig    TokenSource = "gh config"
	TokenSourceGHCLI       TokenSource = "gh auth token"
)

// Token is a resolved credential and the source that produced it.
//
// Value is never rendered by either of the two ways a struct usually reaches an
// output stream: [Token.String] covers the fmt verbs and [Token.LogValue] covers
// slog. A credential that leaks into a debug log is not recoverable by editing
// the log, so the redaction lives on the type rather than in the discipline of
// every call site.
type Token struct {
	// Value is the credential itself.
	Value string

	// Source is the step of the chain that produced Value.
	Source TokenSource
}

// String implements fmt.Stringer, redacting the credential.
func (t Token) String() string { return fmt.Sprintf("token from %s", t.Source) }

// LogValue implements slog.LogValuer, redacting the credential.
func (t Token) LogValue() slog.Value { return slog.StringValue(t.String()) }

// Resolver resolves a GitHub token from the four sources the design fixes, in
// order.
//
// The three function fields are the seams the tests drive: with all of them nil
// a zero Resolver reads the real environment, the real gh config, and the real
// gh binary, which is what production wants. Only Flag is ordinary data.
type Resolver struct {
	// Flag is the value of --token, or "" when the flag was not passed.
	Flag string

	// LookupEnv reads an environment variable. nil means os.LookupEnv.
	LookupEnv func(key string) (value string, ok bool)

	// ConfigToken reads the token the gh config file holds for a host, and the
	// name of the config key it came from. nil means go-gh's auth.TokenForHost.
	ConfigToken func(host string) (token string, source string)

	// CLIToken runs `gh auth token` for a host. nil means the real shell-out.
	CLIToken func(ctx context.Context, host string) (string, error)
}

// tokenStep is one link in the chain: a source to report and a way to get a
// candidate value for it.
type tokenStep struct {
	source TokenSource
	token  func(ctx context.Context) (string, error)
}

// Resolve walks the chain and returns the first non-empty token it finds.
//
// A step that fails is not fatal and does not end the walk: `gh` not being
// installed is the ordinary case on a CI runner, not an error worth reporting to
// someone who set GITHUB_TOKEN anyway. Failures are logged at debug level and
// the walk continues, so the only outcome a user ever sees is a token or
// [labelsync.ErrNoToken].
func (r Resolver) Resolve(ctx context.Context) (Token, error) {
	for _, step := range r.steps() {
		value, err := step.token(ctx)
		if err != nil {
			slog.Debug("token source failed", "source", string(step.source), "error", err)

			continue
		}

		// Trimmed before the emptiness test, not after it: `gh auth token` ends
		// its output with a newline, and an environment variable set to a stray
		// space is set to nothing useful.
		value = strings.TrimSpace(value)
		if value == "" {
			slog.Debug("token source empty", "source", string(step.source))

			continue
		}

		slog.Debug("token resolved", "source", string(step.source))

		return Token{Value: value, Source: step.source}, nil
	}

	return Token{}, fmt.Errorf("%w: %s", labelsync.ErrNoToken, noTokenHelp)
}

// steps builds the chain in resolution order.
func (r Resolver) steps() []tokenStep {
	return []tokenStep{
		{
			source: TokenSourceFlag,
			token:  func(context.Context) (string, error) { return r.Flag, nil },
		},
		{
			source: TokenSourceGHToken,
			token:  func(context.Context) (string, error) { return r.env(string(TokenSourceGHToken)) },
		},
		{
			source: TokenSourceGitHubToken,
			token:  func(context.Context) (string, error) { return r.env(string(TokenSourceGitHubToken)) },
		},
		{
			source: TokenSourceGHConfig,
			token: func(context.Context) (string, error) {
				token, _ := r.configToken(Host)

				return token, nil
			},
		},
		{
			source: TokenSourceGHCLI,
			token:  func(ctx context.Context) (string, error) { return r.cliToken(ctx, Host) },
		},
	}
}

// env reads an environment variable. An unset variable and one set to the empty
// string are the same absence as far as the chain is concerned, so ok is
// discarded rather than distinguished.
func (r Resolver) env(key string) (string, error) {
	if r.LookupEnv != nil {
		value, _ := r.LookupEnv(key)

		return value, nil
	}

	return os.Getenv(key), nil
}

// configToken reads the gh config file through go-gh.
func (r Resolver) configToken(host string) (string, string) {
	if r.ConfigToken != nil {
		return r.ConfigToken(host)
	}

	return auth.TokenForHost(host)
}

// cliToken shells out to gh.
func (r Resolver) cliToken(ctx context.Context, host string) (string, error) {
	if r.CLIToken != nil {
		return r.CLIToken(ctx, host)
	}

	return ghAuthToken(ctx, host)
}

// ghAuthToken runs `gh auth token --hostname <host>`.
//
// # This step is not redundant
//
// It looks like dead code sitting behind step 3, and it is not. Modern gh stores
// its token in the system keychain by default on macOS, and a keychain token is
// not written to hosts.yml — so go-gh's config reader returns nothing for a user
// who is perfectly well logged in. gh itself knows how to read its own keychain
// entry, and asking it is the only way to reach that token.
//
// Keeping both steps means the config file covers the case where gh is not on
// PATH (a CI image with a hosts.yml baked in), and the shell-out covers the case
// where the token is not in the config file (a developer laptop). Neither
// subsumes the other.
func ghAuthToken(ctx context.Context, host string) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host).Output()
	if err != nil {
		// Output captures stderr into the ExitError, and gh puts its actual
		// complaint there — "not logged into any GitHub hosts" is a far more
		// useful debug line than "exit status 1".
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("gh auth token: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}

		return "", fmt.Errorf("gh auth token: %w", err)
	}

	return string(out), nil
}
