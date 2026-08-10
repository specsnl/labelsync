package cmd

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/specsnl/labelsync/internal/github"
	"github.com/specsnl/labelsync/internal/labelsync"
	"github.com/specsnl/labelsync/internal/util/output"
)

// cacheInfoRow is `cache info` for both audiences.
//
// Every field is the machine's value: bytes as a number, the oldest entry as an
// RFC 3339 timestamp. "1.2 MiB" and "3 days ago" are the *table's* rendering of
// the same two fields and live in the columns, because a struct field
// pre-formatted into a string is a number a consumer can no longer compare. The
// json tags are a public contract: added to, never renamed.
type cacheInfoRow struct {
	Path    string `json:"path"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
	Schema  int    `json:"schema"`

	// Oldest is RFC 3339, and omitted entirely when the cache is empty — an
	// entry age for a cache with no entries would be a value with nothing behind
	// it.
	Oldest string `json:"oldest,omitempty"`
}

// cacheClearRow is `cache clear`: what was removed, in the same terms.
type cacheClearRow struct {
	Path    string `json:"path"`
	Removed int    `json:"removed"`
	Bytes   int64  `json:"bytes"`
}

func newCacheCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and clear the ETag cache",
		Long: `Manage the ETag store that makes repeat dry-runs effectively free.

A conditional request that comes back 304 Not Modified does not count against
GitHub's primary rate limit, so a cached label list costs nothing to re-read.
Labels change rarely, which is what makes "labelsync sync --dry-run" cheap enough
to run on every pull request.

Nothing here is required for correctness: --no-cache skips the cache for a single
run, a corrupt entry is a miss rather than an error, and clearing it only costs
the next run one request per repository.`,
	}

	cmd.AddCommand(newCacheInfoCmd(app), newCacheClearCmd(app))

	return cmd
}

func newCacheInfoCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Report where the cache is and what is in it",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runCacheInfo(app)
		},
	}
}

func newCacheClearCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove every cached label list",
		Long: `Remove every cached label list.

Only the entries labelsync itself wrote, in the resolved cache directory, and
never the directory or anything under it. Clearing a cache that is already empty
is a no-op rather than an error.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return runCacheClear(app)
		},
	}
}

// runCacheInfo describes the cache.
func runCacheInfo(app *App) error {
	store, err := app.store()
	if err != nil {
		return err
	}

	info, err := store.Info()
	if err != nil {
		return err
	}

	row := cacheInfoRow{
		Path:    info.Dir,
		Entries: info.Entries,
		Bytes:   info.Bytes,
		Schema:  info.Schema,
	}

	if !info.Oldest.IsZero() {
		row.Oldest = info.Oldest.Format(time.RFC3339)
	}

	now := app.now()

	output.Table(app.Out, []cacheInfoRow{row},
		output.Col("Location", func(r cacheInfoRow) string { return r.Path }),
		output.Col("Entries", func(r cacheInfoRow) string { return strconv.Itoa(r.Entries) }),
		output.Col("Size", func(r cacheInfoRow) string { return humanBytes(r.Bytes) }),
		output.Col("Schema", func(r cacheInfoRow) string { return strconv.Itoa(r.Schema) }),
		output.Col("Oldest entry", func(cacheInfoRow) string { return humanAge(now, info.Oldest) }),
	)

	return nil
}

// runCacheClear empties the cache.
func runCacheClear(app *App) error {
	store, err := app.store()
	if err != nil {
		return err
	}

	cleared, err := store.Clear()
	if err != nil {
		return err
	}

	output.Table(app.Out, []cacheClearRow{{
		Path:    cleared.Dir,
		Removed: cleared.Entries,
		Bytes:   cleared.Bytes,
	}},
		output.Col("Location", func(r cacheClearRow) string { return r.Path }),
		output.Col("Removed", func(r cacheClearRow) string { return strconv.Itoa(r.Removed) }),
		output.Col("Freed", func(r cacheClearRow) string { return humanBytes(r.Bytes) }),
	)

	return nil
}

// store opens the cache directory, bounded by the XDG cache home.
//
// The bound is passed in rather than derived where it is checked: this is the
// command that deletes things, and the path it deletes comes from the
// environment. See github.OpenStore.
func (a *App) store() (*github.Store, error) {
	dir, root := a.CacheDir, a.CacheRoot

	if dir == "" {
		dir = labelsync.CacheDir()
	}

	if root == "" {
		root = labelsync.CacheRoot()
	}

	return github.OpenStore(dir, root)
}

// byteUnits are the binary units `cache info` renders sizes in. GitHub's label
// lists are small, so the tail is never reached and is there for completeness
// rather than for use.
var byteUnits = []string{"KiB", "MiB", "GiB", "TiB"}

// humanBytes renders a size for the table. The record keeps the int64: this is
// the reading, not the value.
func humanBytes(bytes int64) string {
	const unit = 1024

	if bytes < unit {
		return strconv.FormatInt(bytes, 10) + " B"
	}

	size := float64(bytes)

	for _, suffix := range byteUnits {
		size /= unit

		if math.Abs(size) < unit {
			return fmt.Sprintf("%.1f %s", size, suffix)
		}
	}

	return fmt.Sprintf("%.1f %s", size, byteUnits[len(byteUnits)-1])
}

// humanAge renders how long ago a timestamp was, for the table. The record keeps
// RFC 3339.
//
// Rounded to the coarsest unit that says something, because the question behind
// the column is "is this cache stale" and not "how many minutes".
func humanAge(now, then time.Time) string {
	if then.IsZero() {
		return "—"
	}

	elapsed := now.Sub(then)

	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return plural(int(elapsed.Minutes()), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return plural(int(elapsed.Hours()), "hour") + " ago"
	default:
		return plural(int(elapsed.Hours()/24), "day") + " ago"
	}
}

// plural renders a count with its noun, singular at one.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(n) + " " + noun + "s"
}
