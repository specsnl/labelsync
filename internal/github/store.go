// store.go is the ETag cache seen from outside: what is in it, and how to empty
// it. cache.go is the same directory seen from the read path.
//
// # The bound is explicit
//
// `labelsync cache clear` takes a path that ultimately comes from the
// environment — XDG_CACHE_HOME — and then deletes what is in it. Nothing about
// that is safe by construction, so [OpenStore] takes the root it must sit under
// as an argument and refuses anything else. A bound derived inside this file
// from the same environment variable would not be a bound at all.
package github

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/specsnl/labelsync/internal/labelsync"
)

// CacheSchema is the version of the on-disk entry shape, as `cache info`
// reports it. See [cacheSchema].
const CacheSchema = cacheSchema

// entryPattern matches the file name [cache.path] produces: the SHA-256 of the
// lowercased owner/repo, in hex.
//
// Clearing matches on it rather than deleting whatever is in the directory,
// because a cache directory a user has pointed at something else is exactly the
// case the guard exists for and the guard cannot catch a path that is
// legitimately under the cache home. Between the two, nothing this tool did not
// write is ever removed.
var entryPattern = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

// tempPrefix is what an interrupted write leaves behind — os.CreateTemp with the
// pattern cache.save uses. They are ours, so clearing removes them too.
const tempPrefix = ".tmp-"

// Store is the ETag cache as the cache commands see it: a bounded directory that
// can be described and emptied.
type Store struct {
	dir  string
	root string
}

// CacheInfo is what `cache info` reports.
//
// The types are the machine's: bytes as an int64 and a timestamp as a
// time.Time, never "1.2 MiB" and "3 days ago". Those are the table's rendering
// of the same values, and putting them in the struct would make them the only
// thing a JSON consumer could have.
type CacheInfo struct {
	// Dir is the cache directory, whether or not it exists yet.
	Dir string

	// Entries is how many cached label lists are in it.
	Entries int

	// Bytes is their total size on disk.
	Bytes int64

	// Schema is the entry-shape version this binary writes and accepts.
	Schema int

	// Oldest is the modification time of the oldest entry, and the zero time
	// when there are none.
	Oldest time.Time
}

// CacheCleared is what `cache clear` removed.
type CacheCleared struct {
	Dir     string
	Entries int
	Bytes   int64
}

// OpenStore returns a handle on the cache directory at dir, which must sit
// **inside** root.
//
// root is [labelsync.CacheRoot] in production and a temporary directory under
// test. Passing it in rather than reading it here is the whole point: this is
// the function that decides whether a delete is allowed, and a check that
// derives its own bound from the same environment variable the path came from
// checks nothing.
//
// A directory that does not exist yet is fine — an empty cache is a cache — and
// is reported as zero entries rather than as a failure.
func OpenStore(dir, root string) (*Store, error) {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: %q is not inside %q", labelsync.ErrUnsafeCacheDir, dir, root)
	}

	cleanDir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("resolving the cache directory: %w", err)
	}

	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolving the cache home: %w", err)
	}

	// Rel rather than a string prefix: "/home/u/.cache-of-somebody-else" has
	// "/home/u/.cache" as a prefix and is not inside it. A ".." in the result is
	// a path that climbs out, and "." is the root itself — which is somebody's
	// whole cache home, and not ours to empty.
	rel, err := filepath.Rel(cleanRoot, cleanDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: %q is not inside %q", labelsync.ErrUnsafeCacheDir, cleanDir, cleanRoot)
	}

	return &Store{dir: cleanDir, root: cleanRoot}, nil
}

// Dir is the directory the store is bound to.
func (s *Store) Dir() string { return s.dir }

// Info describes what is in the cache.
func (s *Store) Info() (CacheInfo, error) {
	info := CacheInfo{Dir: s.dir, Schema: CacheSchema}

	entries, err := s.entries()
	if err != nil {
		return CacheInfo{}, err
	}

	for _, entry := range entries {
		stat, err := entry.Info()
		if err != nil {
			// The file went away between the listing and the stat, which is
			// another labelsync clearing the cache. Not this run's problem.
			continue
		}

		info.Entries++
		info.Bytes += stat.Size()

		if info.Oldest.IsZero() || stat.ModTime().Before(info.Oldest) {
			info.Oldest = stat.ModTime()
		}
	}

	return info, nil
}

// Clear removes every cache entry, and reports what went.
//
// The directory itself stays, and nothing is recursed into: only the entry files
// this tool writes, in this one directory, are ever removed. An empty or absent
// cache is a no-op rather than an error — clearing a cache that is already clear
// is exactly what a user asking for a clean run means.
func (s *Store) Clear() (CacheCleared, error) {
	cleared := CacheCleared{Dir: s.dir}

	entries, err := s.entries()
	if err != nil {
		return CacheCleared{}, err
	}

	for _, entry := range entries {
		path := filepath.Join(s.dir, entry.Name())

		size := int64(0)
		if stat, err := entry.Info(); err == nil {
			size = stat.Size()
		}

		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}

			return CacheCleared{}, fmt.Errorf("removing %s: %w", path, err)
		}

		cleared.Entries++
		cleared.Bytes += size
	}

	return cleared, nil
}

// entries lists the files in the cache directory that this tool wrote.
//
// os.ReadDir rather than a walk: the cache is flat, and recursing would be the
// difference between removing our own files and removing whatever a user had
// pointed the variable at.
func (s *Store) entries() ([]os.DirEntry, error) {
	listed, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("reading %s: %w", s.dir, err)
	}

	out := make([]os.DirEntry, 0, len(listed))

	for _, entry := range listed {
		if entry.IsDir() || !ours(entry.Name()) {
			continue
		}

		out = append(out, entry)
	}

	return out, nil
}

// ours reports whether a file name is one the cache wrote: an entry, or the
// temporary file an interrupted write leaves behind.
func ours(name string) bool {
	return entryPattern.MatchString(name) || strings.HasPrefix(name, tempPrefix)
}
