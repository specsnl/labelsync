package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// TestOpenStoreBoundsTheDirectory pins the check that stands between a
// mistyped environment variable and a delete. The prefix cases are the ones
// worth having: "/tmp/cache-of-somebody-else" has "/tmp/cache" as a string
// prefix and is not inside it, which is why the check is filepath.Rel and not
// strings.HasPrefix.
func TestOpenStoreBoundsTheDirectory(t *testing.T) {
	root := t.TempDir()

	tests := map[string]struct {
		dir  string
		want bool
	}{
		"the cache directory itself":      {dir: filepath.Join(root, "labelsync"), want: true},
		"deeper inside":                   {dir: filepath.Join(root, "labelsync", "v2"), want: true},
		"the cache home itself":           {dir: root, want: false},
		"a sibling sharing a prefix":      {dir: root + "-of-somebody-else", want: false},
		"a path climbing out":             {dir: filepath.Join(root, "..", "elsewhere"), want: false},
		"an unrelated absolute path":      {dir: filepath.Join(t.TempDir(), "labelsync"), want: false},
		"empty":                           {dir: "", want: false},
		"a dot-segment that resolves out": {dir: filepath.Join(root, "labelsync", "..", ".."), want: false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := OpenStore(tc.dir, root)

			if tc.want {
				if err != nil {
					t.Fatalf("OpenStore(%q, %q) = %v, want it allowed", tc.dir, root, err)
				}

				return
			}

			if !errors.Is(err, labelsync.ErrUnsafeCacheDir) {
				t.Fatalf("OpenStore(%q, %q) = (%v, %v), want ErrUnsafeCacheDir", tc.dir, root, store, err)
			}
		})
	}
}

// An interrupted write leaves a temporary file behind. It is ours, so clearing
// removes it — otherwise a cache that had been killed mid-write would never come
// back to empty.
func TestStoreClearRemovesTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "labelsync")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("creating the cache directory: %v", err)
	}

	entry := filepath.Join(dir, strings.Repeat("a", 64)+".json")
	temp := filepath.Join(dir, ".tmp-123456")

	for _, path := range []string{entry, temp} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatalf("seeding %s: %v", path, err)
		}
	}

	store, err := OpenStore(dir, root)
	if err != nil {
		t.Fatalf("OpenStore() = %v, want nil", err)
	}

	cleared, err := store.Clear()
	if err != nil {
		t.Fatalf("Clear() = %v, want nil", err)
	}

	if cleared.Entries != 2 {
		t.Errorf("Clear() removed %d files, want 2 (the entry and the temporary file)", cleared.Entries)
	}
}

// The store and the read path have to agree on the file name, or `cache info`
// reports an empty cache the next dry run then hits. This writes an entry
// through the cache and reads it back through the store.
func TestStoreSeesWhatTheCacheWrote(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "labelsync")

	newCache(dir).save("specsnl/labelsync", `"etag"`, 1, []Label{{Name: "bug", Color: "d73a4a"}})

	store, err := OpenStore(dir, root)
	if err != nil {
		t.Fatalf("OpenStore() = %v, want nil", err)
	}

	info, err := store.Info()
	if err != nil {
		t.Fatalf("Info() = %v, want nil", err)
	}

	if info.Entries != 1 {
		t.Fatalf("Info().Entries = %d, want 1 — the store and the cache disagree on the file name", info.Entries)
	}

	if info.Bytes <= 0 {
		t.Errorf("Info().Bytes = %d, want the entry's size", info.Bytes)
	}

	if info.Oldest.IsZero() {
		t.Error("Info().Oldest is zero, want the entry's modification time")
	}

	if info.Schema != CacheSchema {
		t.Errorf("Info().Schema = %d, want %d", info.Schema, CacheSchema)
	}
}
