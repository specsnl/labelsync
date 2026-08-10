package cmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/specsnl/labelsync/internal/cmd"
	"github.com/specsnl/labelsync/internal/labelsync"
)

// fixedNow is the clock every test here runs against. An age rendered against
// the real clock is not something a test can assert.
var fixedNow = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)

// cacheApp builds an App whose cache commands are pointed at a temporary
// directory inside a temporary root, and returns both paths.
//
// Never the developer's real cache: `cache clear` deletes what it finds, and a
// suite that ran against the real one would empty it.
func cacheApp(t *testing.T) (*cmd.App, string) {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, "labelsync")

	app := cmd.NewApp()
	app.CacheRoot = root
	app.CacheDir = dir
	app.Now = func() time.Time { return fixedNow }

	return app, dir
}

// seedCache writes count entries with names shaped the way the cache writes
// them, the oldest of them age old.
func seedCache(t *testing.T, dir string, count int, age time.Duration) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the cache directory: %v", err)
	}

	for i := range count {
		// 64 hex characters, which is what a SHA-256 of owner/repo renders as.
		name := strings.Repeat(string(rune('a'+i)), 64) + ".json"
		path := filepath.Join(dir, name)

		if err := os.WriteFile(path, []byte(`{"schema":1,"repo":"specsnl/labelsync"}`), 0o600); err != nil {
			t.Fatalf("seeding a cache entry: %v", err)
		}

		when := fixedNow.Add(-age).Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatalf("dating a cache entry: %v", err)
		}
	}
}

// TestCacheInfo_ReportsAPopulatedCache covers the split the row struct exists
// for: the JSON record carries the numbers, the table carries the readings.
func TestCacheInfo_ReportsAPopulatedCache(t *testing.T) {
	app, dir := cacheApp(t)
	seedCache(t, dir, 3, 72*time.Hour)

	_, stdout, _, err := runApp(t, app, nil, "--output", "json", "cache", "info")
	if err != nil {
		t.Fatalf("cache info: %v", err)
	}

	lines := jsonLines(t, stdout)
	if len(lines) != 1 {
		t.Fatalf("stdout = %q, want one object", stdout)
	}

	row := lines[0]

	if got := row["path"]; got != dir {
		t.Errorf("path = %v, want %q", got, dir)
	}

	if got := row["entries"]; got != float64(3) {
		t.Errorf("entries = %v (%T), want 3 as a number", got, got)
	}

	// A number, not "117 B": a consumer has to be able to compare it.
	bytes, ok := row["bytes"].(float64)
	if !ok || bytes <= 0 {
		t.Errorf("bytes = %v (%T), want a positive number", row["bytes"], row["bytes"])
	}

	if got := row["schema"]; got != float64(1) {
		t.Errorf("schema = %v, want the current schema version as a number", got)
	}

	// RFC 3339, not "3 days ago".
	oldest, _ := row["oldest"].(string)
	if _, err := time.Parse(time.RFC3339, oldest); err != nil {
		t.Errorf("oldest = %q, want RFC 3339: %v", oldest, err)
	}
}

// The table is the other projection of the same row: a size a human reads and an
// age in words, neither of which is in the record.
func TestCacheInfo_PrettyRendering(t *testing.T) {
	app, dir := cacheApp(t)
	seedCache(t, dir, 2, 72*time.Hour)

	_, stdout, _, err := runApp(t, app, nil, "cache", "info")
	if err != nil {
		t.Fatalf("cache info: %v", err)
	}

	for _, want := range []string{"Location", "Entries", "Size", "Schema", "Oldest entry", " B", "3 days ago"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
}

// An empty cache is a cache. It reports zero rather than failing, and says so
// where the age would be rather than leaving a blank a reader has to interpret.
func TestCacheInfo_EmptyCache(t *testing.T) {
	app, _ := cacheApp(t)

	_, stdout, _, err := runApp(t, app, nil, "--output", "json", "cache", "info")
	if err != nil {
		t.Fatalf("cache info on an absent cache: %v", err)
	}

	row := jsonLines(t, stdout)[0]

	if got := row["entries"]; got != float64(0) {
		t.Errorf("entries = %v, want 0", got)
	}

	// An entry age for a cache with no entries would be a value with nothing
	// behind it.
	if _, present := row["oldest"]; present {
		t.Errorf("oldest = %v, want it absent for an empty cache", row["oldest"])
	}
}

// Clearing removes the entries and reports what went.
func TestCacheClear_RemovesEntries(t *testing.T) {
	app, dir := cacheApp(t)
	seedCache(t, dir, 3, time.Hour)

	_, stdout, _, err := runApp(t, app, nil, "--output", "json", "cache", "clear")
	if err != nil {
		t.Fatalf("cache clear: %v", err)
	}

	if got := jsonLines(t, stdout)[0]["removed"]; got != float64(3) {
		t.Errorf("removed = %v, want 3", got)
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the cache directory: %v", err)
	}

	if len(left) != 0 {
		t.Errorf("the cache still holds %d files", len(left))
	}

	// The directory itself stays: it is where the next run writes.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the cache directory was removed: %v", err)
	}
}

// Clearing a cache that is already clear is what a user asking for a clean run
// means, not a failure.
func TestCacheClear_IsANoOpOnAnEmptyCache(t *testing.T) {
	app, _ := cacheApp(t)

	_, stdout, _, err := runApp(t, app, nil, "--output", "json", "cache", "clear")
	if err != nil {
		t.Fatalf("cache clear on an absent cache: %v", err)
	}

	if got := jsonLines(t, stdout)[0]["removed"]; got != float64(0) {
		t.Errorf("removed = %v, want 0", got)
	}
}

// Only the files labelsync wrote. A cache directory holding something else is
// exactly the case the guard cannot catch — the path is legitimately under the
// cache home — so the name is checked too.
func TestCacheClear_LeavesFilesItDidNotWrite(t *testing.T) {
	app, dir := cacheApp(t)
	seedCache(t, dir, 1, time.Hour)

	stranger := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stranger, []byte("not ours"), 0o600); err != nil {
		t.Fatalf("seeding a stranger: %v", err)
	}

	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("seeding a nested directory: %v", err)
	}

	if _, _, _, err := runApp(t, app, nil, "cache", "clear"); err != nil {
		t.Fatalf("cache clear: %v", err)
	}

	for _, path := range []string{stranger, nested} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
}

// The path guard is the point of the whole file: this command takes a path from
// the environment and then deletes what is in it, so the bound is explicit.
func TestCache_RefusesADirectoryOutsideTheCacheHome(t *testing.T) {
	root := t.TempDir()

	for name, dir := range map[string]string{
		"a sibling of the cache home": t.TempDir(),
		"the cache home itself":       root,
		"a path climbing out":         filepath.Join(root, "..", "elsewhere"),
		"empty":                       "",
	} {
		t.Run(name, func(t *testing.T) {
			for _, sub := range []string{"info", "clear"} {
				app := cmd.NewApp()
				app.CacheRoot = root
				app.CacheDir = dir

				_, _, _, err := runApp(t, app, nil, "cache", sub)
				if !errors.Is(err, labelsync.ErrUnsafeCacheDir) {
					t.Errorf("cache %s: error = %v, want one wrapping ErrUnsafeCacheDir", sub, err)
				}

				if kind := labelsync.KindOf(err); kind != "unsafe_cache_dir" {
					t.Errorf("cache %s: error_kind = %q, want %q", sub, kind, "unsafe_cache_dir")
				}
			}
		})
	}
}

// A refused directory is refused before anything is removed. The guard would be
// worth little if it reported afterwards.
func TestCache_RefusalRemovesNothing(t *testing.T) {
	outside := t.TempDir()

	path := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seeding the file: %v", err)
	}

	app := cmd.NewApp()
	app.CacheRoot = t.TempDir()
	app.CacheDir = outside

	if _, _, _, err := runApp(t, app, nil, "cache", "clear"); err == nil {
		t.Fatal("want a refusal, got none")
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("the file was removed anyway: %v", err)
	}
}
