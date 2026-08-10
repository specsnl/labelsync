// cache.go is the ETag store behind conditional label reads.
//
// It is the single most valuable optimisation in the tool: a conditional
// request that comes back 304 Not Modified does not count against the primary
// rate limit. Labels change rarely, so hit rates are high, which is what makes
// `sync --dry-run` cheap enough to run on every pull request.
//
// # The cache may never fail a run
//
// It is an optimisation, and an optimisation that can break a run is a
// liability. Every failure here — an unreadable directory, a truncated file,
// half-written JSON, an entry from an older labelsync — is a miss. Nothing in
// this file returns an error to a caller, and the only thing a caller can do
// wrong is not use it.
package github

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// cacheSchema is the version of the on-disk entry shape.
//
// It is stored in every entry and checked on load, so an upgrade that changes
// what an entry holds invalidates cleanly rather than deserialising a stale
// shape into a new struct and planning from it. Bump it whenever [cacheEntry]
// changes meaning — a new field with a zero value that reads as "false" is
// exactly the case this exists for.
const cacheSchema = 1

// cacheEntry is one repository's cached label list.
type cacheEntry struct {
	Schema int    `json:"schema"`
	Repo   string `json:"repo"` // owner/repo, for reading the file by eye
	ETag   string `json:"etag"`

	// Pages is how many pages the list took to read. An ETag covers the
	// response it came from — page one — and a repository with more than one
	// page of labels can change beyond that page without page one's
	// representation changing at all. Serving such a list from cache would plan
	// creates for labels that already exist, so only a single-page list is ever
	// served from here. See [Client.ListLabels].
	Pages int `json:"pages"`

	Labels []Label `json:"labels"`
}

// cache is a directory of cached label lists, or nothing at all.
//
// A nil *cache is a working cache that never hits and never stores, which is
// what --no-cache resolves to. Every method is nil-safe for that reason: the
// read path should not be littered with checks for whether caching is on.
type cache struct {
	dir string
}

// newCache returns a cache rooted at dir, or nil when dir is empty. Empty is how
// --no-cache arrives here: not a flag threaded through the read path, but the
// absence of anywhere to put anything.
func newCache(dir string) *cache {
	if strings.TrimSpace(dir) == "" {
		return nil
	}

	return &cache{dir: dir}
}

// load returns the entry for repo, and whether it is usable.
//
// Anything unusable — absent, unreadable, corrupt, or written by a labelsync
// with a different schema — is a miss. A miss costs one live request; treating
// any of it as an error would cost the run.
func (c *cache) load(repo string) (cacheEntry, bool) {
	if c == nil {
		return cacheEntry{}, false
	}

	raw, err := os.ReadFile(c.path(repo))
	if err != nil {
		return cacheEntry{}, false
	}

	var entry cacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		slog.Debug("discarding a corrupt cache entry", "repo", repo, "error", err)

		return cacheEntry{}, false
	}

	if entry.Schema != cacheSchema || entry.ETag == "" {
		slog.Debug("discarding a stale cache entry", "repo", repo, "schema", entry.Schema, "want", cacheSchema)

		return cacheEntry{}, false
	}

	return entry, true
}

// save stores the labels read for repo under etag. A failure to write is logged
// and otherwise ignored: the run has the labels already, and the only thing lost
// is the next run's saving.
//
// The write goes to a temporary file and is renamed into place, so a run
// interrupted mid-write leaves the previous entry rather than a half-written one
// that the next run has to recognise as corrupt.
func (c *cache) save(repo, etag string, pages int, labels []Label) {
	if c == nil || etag == "" {
		return
	}

	entry := cacheEntry{
		Schema: cacheSchema,
		Repo:   repo,
		ETag:   etag,
		Pages:  pages,
		Labels: labels,
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		slog.Debug("not caching the label list", "repo", repo, "error", err)

		return
	}

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		slog.Debug("not caching the label list", "repo", repo, "error", err)

		return
	}

	path := c.path(repo)

	tmp, err := os.CreateTemp(c.dir, ".tmp-*")
	if err != nil {
		slog.Debug("not caching the label list", "repo", repo, "error", err)

		return
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()

		slog.Debug("not caching the label list", "repo", repo, "error", err)

		return
	}

	if err := tmp.Close(); err != nil {
		slog.Debug("not caching the label list", "repo", repo, "error", err)

		return
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		slog.Debug("not caching the label list", "repo", repo, "error", err)
	}
}

// path is where repo's entry lives.
//
// The name is the SHA-256 of the lowercased owner/repo rather than the reference
// itself. A repository name is not a file name — it carries slashes, and
// GitHub's rules are not the file system's — and a cache key that can escape its
// own directory is not a cache key. Lowercasing keeps `Owner/Repo` and
// `owner/repo`, which address the same repository, on one entry. The reference
// is stored inside the file, so an entry is still identifiable by eye.
func (c *cache) path(repo string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(repo)))

	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}
