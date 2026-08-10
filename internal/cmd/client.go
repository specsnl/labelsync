package cmd

import (
	"context"
	"log/slog"

	"github.com/specsnl/labelsync/internal/config"
	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/github/ratelimit"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// client resolves a token and builds the GitHub client every network command
// runs on, from the persistent flags and nothing else.
//
// It is one function so that --token, --no-cache, --write-rate, and --max-wait
// mean the same thing in every command. A command that built its own client
// would be a command where one of those flags quietly did nothing.
func (a *App) client(ctx context.Context) (*github.Client, error) {
	token, err := github.Resolver{Flag: a.Token}.Resolve(ctx)
	if err != nil {
		return nil, err
	}

	opts := []github.Option{
		github.WithLimiter(ratelimit.New(
			ratelimit.WithWriteRate(a.WriteRate),
			ratelimit.WithMaxWait(a.MaxWait),
		)),
	}

	// --no-cache arrives as the absence of anywhere to put anything, rather than
	// as a flag threaded through the read path. See github.WithCacheDir.
	if !a.NoCache {
		opts = append(opts, github.WithCacheDir(labelsync.CacheDir()))
	}

	// Last, so a test can override any of the above — a base URL pointing at
	// httptest, a cache directory under t.TempDir() — through one seam.
	return github.New(token, append(opts, a.GitHub...)...)
}

// resolve loads the config and resolves its groups into selectors, reporting
// whatever the resolution wanted said out loud.
//
// The authenticated login is only fetched when the config actually has a `user:`
// group, because it costs a request and decides nothing for a config without
// one. A login that cannot be read is not fatal: Resolve reads an empty login as
// "somebody else", which is the conservative answer and the one a token that
// cannot see /user deserves.
func (a *App) resolve(ctx context.Context, client *github.Client, cfg *config.Config) (*config.Resolution, error) {
	var login string

	if hasUserGroup(cfg) {
		resolved, err := client.Login(ctx)
		if err != nil {
			slog.Debug("continuing without the authenticated login", "error", err)
		}

		login = resolved
	}

	resolution, err := cfg.Resolve(login)
	if err != nil {
		return nil, err
	}

	// Warnings are collected by config, which has no writer, and printed here,
	// which knows whether the run is pretty or NDJSON. A selector that will
	// select nothing has to say so — quietly resolving to an empty set is the
	// failure mode this exists to prevent.
	for _, warning := range resolution.Warnings() {
		a.Out.Warn("%s", warning)
	}

	return resolution, nil
}

// hasUserGroup reports whether any group enumerates a user, which is the only
// reason a run needs to know who the token belongs to.
func hasUserGroup(cfg *config.Config) bool {
	for _, group := range cfg.Groups {
		if group.User != "" {
			return true
		}
	}

	return false
}
