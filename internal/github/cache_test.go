package github

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// labelBody is one page of labels, and the body every handler here answers with
// unless it is answering 304.
const labelBody = `[{"name":"bug","color":"d73a4a","description":"Something is broken"}]`

// cachingClient points a client at a test server and at dir, recording what the
// server was asked. The If-None-Match header is the assertion most of these
// tests turn on: it is what makes a request conditional, and a conditional
// request that comes back 304 costs no quota.
func cachingClient(t *testing.T, dir string, handler http.HandlerFunc) (*Client, *[]string) {
	t.Helper()

	var conditionals []string

	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conditionals = append(conditionals, r.Header.Get("If-None-Match"))

		handler(w, r)
	}), WithCacheDir(dir))

	return client, &conditionals
}

// entriesIn returns the cache files in dir, which is how "was anything stored"
// is asked without knowing the hashed name.
func entriesIn(t *testing.T, dir string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatalf("globbing the cache directory: %v", err)
	}

	return matches
}

// TestCachePopulatesThenServesA304 is the optimisation itself: the first read
// stores an ETag, the second offers it, and the 304 is answered from disk
// without the labels being sent again.
func TestCachePopulatesThenServesA304(t *testing.T) {
	dir := t.TempDir()

	client, conditionals := cachingClient(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc123"` {
			w.Header().Set("ETag", `"abc123"`)
			w.WriteHeader(http.StatusNotModified)

			return
		}

		w.Header().Set("ETag", `"abc123"`)
		write(w, labelBody)
	})

	first, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
	if err != nil {
		t.Fatalf("first ListLabels() error = %v, want nil", err)
	}

	if len(entriesIn(t, dir)) != 1 {
		t.Fatalf("cache entries after the first read = %d, want 1", len(entriesIn(t, dir)))
	}

	second, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
	if err != nil {
		t.Fatalf("second ListLabels() error = %v, want nil", err)
	}

	if len(second) != len(first) || (len(second) > 0 && second[0] != first[0]) {
		t.Errorf("cached labels = %+v, want the same as the live read %+v", second, first)
	}

	if got := *conditionals; len(got) != 2 || got[0] != "" || got[1] != `"abc123"` {
		t.Errorf("If-None-Match headers = %q, want [\"\", \"abc123\"]", got)
	}

	// A 304 is not a failure, and recording one would put a healthy repository
	// in the end-of-run summary of skipped ones.
	if got := client.Failures().Len(); got != 0 {
		t.Errorf("Failures().Len() = %d, want 0", got)
	}
}

// TestCacheBypassedWithoutADirectory covers --no-cache, which arrives here as
// the absence of anywhere to put anything: no header goes out, and nothing is
// written.
func TestCacheBypassedWithoutADirectory(t *testing.T) {
	dir := t.TempDir()

	client, conditionals := cachingClient(t, "", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		write(w, labelBody)
	})

	for range 2 {
		if _, err := client.ListLabels(t.Context(), "specsnl", "labelsync"); err != nil {
			t.Fatalf("ListLabels() error = %v, want nil", err)
		}
	}

	for i, got := range *conditionals {
		if got != "" {
			t.Errorf("request %d carried If-None-Match %q, want none", i+1, got)
		}
	}

	if got := entriesIn(t, dir); len(got) != 0 {
		t.Errorf("cache entries = %v, want none written", got)
	}
}

// TestCacheMissesAreNeverErrors covers the three ways an entry can be unusable.
// The cache is an optimisation, and an optimisation that can fail a run is a
// liability: each of these costs one live request and nothing else.
func TestCacheMissesAreNeverErrors(t *testing.T) {
	usable := cacheEntry{
		Schema: cacheSchema,
		Repo:   "specsnl/labelsync",
		ETag:   `"abc123"`,
		Pages:  1,
		Labels: []Label{{Name: "stale", Color: "cccccc"}},
	}

	tests := map[string]func() []byte{
		"a corrupt entry": func() []byte {
			return []byte(`{"schema":1,"etag":"abc123","labels":[{"name":`)
		},
		"an entry from another schema": func() []byte {
			stale := usable
			stale.Schema = cacheSchema + 1

			raw, _ := json.Marshal(stale)

			return raw
		},
		"an entry with no ETag": func() []byte {
			stale := usable
			stale.ETag = ""

			raw, _ := json.Marshal(stale)

			return raw
		},
	}

	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			store := newCache(dir)
			if err := os.WriteFile(store.path("specsnl/labelsync"), corrupt(), 0o600); err != nil {
				t.Fatalf("seeding the cache: %v", err)
			}

			client, conditionals := cachingClient(t, dir, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", `"fresh"`)
				write(w, labelBody)
			})

			labels, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
			if err != nil {
				t.Fatalf("ListLabels() error = %v, want nil: an unusable entry is a miss", err)
			}

			if len(labels) != 1 || labels[0].Name != "bug" {
				t.Errorf("labels = %+v, want the live ones", labels)
			}

			if got := *conditionals; len(got) != 1 || got[0] != "" {
				t.Errorf("If-None-Match headers = %q, want one unconditional request", got)
			}

			// And the miss repairs itself: the entry is rewritten, so the next
			// run is a hit again.
			entry, ok := store.load("specsnl/labelsync")
			if !ok || entry.ETag != `"fresh"` {
				t.Errorf("entry after the read = %+v (usable %t), want the fresh ETag", entry, ok)
			}
		})
	}
}

// TestCacheKeyIsCaseInsensitive covers Owner/Repo and owner/repo, which address
// the same repository and must not be two entries — the second of which would
// never be a hit.
func TestCacheKeyIsCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	store := newCache(dir)

	if got, want := store.path("SpecsNL/LabelSync"), store.path("specsnl/labelsync"); got != want {
		t.Errorf("path(SpecsNL/LabelSync) = %q, want %q", got, want)
	}
}

// TestCacheIsNotServedForAMultiPageList is the correctness limit on the whole
// optimisation. An ETag covers page one, and a repository with more than a
// hundred labels can change beyond it without page one changing — a cached list
// served there would plan creates for labels that already exist.
func TestCacheIsNotServedForAMultiPageList(t *testing.T) {
	dir := t.TempDir()

	client, conditionals := cachingClient(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			write(w, `[{"name":"docs","color":"0075ca"}]`)

			return
		}

		w.Header().Set("ETag", `"page-one"`)
		w.Header().Set("Link", `<https://api.github.com/repositories/1/labels?page=2>; rel="next"`)
		write(w, labelBody)
	})

	if _, err := client.ListLabels(t.Context(), "specsnl", "labelsync"); err != nil {
		t.Fatalf("first ListLabels() error = %v, want nil", err)
	}

	entry, ok := newCache(dir).load("specsnl/labelsync")
	if !ok {
		t.Fatal("nothing was cached, want an entry recording that it took two pages")
	}

	if entry.Pages != 2 {
		t.Errorf("entry.Pages = %d, want 2", entry.Pages)
	}

	if _, err := client.ListLabels(t.Context(), "specsnl", "labelsync"); err != nil {
		t.Fatalf("second ListLabels() error = %v, want nil", err)
	}

	for i, got := range *conditionals {
		if got != "" {
			t.Errorf("request %d carried If-None-Match %q, want none: a multi-page list is read live", i+1, got)
		}
	}
}

// TestCacheOnlyOffersTheETagOnPageOne pins where the header may go. An ETag
// describes the response it came from, so offering page one's on page two asks
// about a response it never described.
func TestCacheOnlyOffersTheETagOnPageOne(t *testing.T) {
	dir := t.TempDir()
	store := newCache(dir)

	// Seeded as a single-page list, which is the state that makes the next read
	// conditional, and then answered with two pages — the shape of a repository
	// that has grown past a hundred labels since the last run.
	store.save("specsnl/labelsync", `"page-one"`, 1, []Label{{Name: "stale"}})

	client, conditionals := cachingClient(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			write(w, `[{"name":"docs","color":"0075ca"}]`)

			return
		}

		w.Header().Set("ETag", `"page-one-again"`)
		w.Header().Set("Link", `<https://api.github.com/repositories/1/labels?page=2>; rel="next"`)
		write(w, labelBody)
	})

	labels, err := client.ListLabels(t.Context(), "specsnl", "labelsync")
	if err != nil {
		t.Fatalf("ListLabels() error = %v, want nil", err)
	}

	if len(labels) != 2 {
		t.Errorf("labels = %+v, want both pages", labels)
	}

	got := *conditionals
	if len(got) != 2 {
		t.Fatalf("requests = %d, want 2", len(got))
	}

	if got[0] != `"page-one"` {
		t.Errorf("page one carried If-None-Match %q, want the cached ETag", got[0])
	}

	if got[1] != "" {
		t.Errorf("page two carried If-None-Match %q, want none", got[1])
	}
}
