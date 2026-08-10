package cmd_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/specsnl/labelsync/internal/cmd"
	"github.com/specsnl/labelsync/internal/github"
)

// testToken is what every end-to-end command test authenticates with. It is
// passed as --token rather than set on the App, because PersistentPreRunE
// overwrites every field a persistent flag owns.
const testToken = "gho_test"

// fakeGitHub builds an App pointed at a test server, and returns it with the
// flags that have to accompany it.
//
// The cache directory is a temporary one rather than off: --no-cache would also
// switch off the conditional requests, and a suite that never exercises the
// cache is a suite that would not notice it breaking. Either way, the developer's
// real cache is never touched — the point of pinning it here.
func fakeGitHub(t *testing.T, handler http.Handler) (*cmd.App, []string) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	app := cmd.NewApp()
	app.GitHub = []github.Option{
		github.WithBaseURL(server.URL),
		github.WithCacheDir(t.TempDir()),
	}

	return app, []string{"--token", testToken}
}

// writeConfig writes a config file into a temporary directory and returns its
// path, for --config.
func writeConfig(t *testing.T, yaml string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "labels.yml")

	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writing the config: %v", err)
	}

	return path
}

// args assembles a command line: the flags fakeGitHub asked for, the config
// path, and then the command's own arguments.
func args(config string, extra []string, rest ...string) []string {
	out := append([]string{}, extra...)
	out = append(out, "--config", config)

	return append(out, rest...)
}

// jsonLines parses NDJSON, failing the test on the first line that is not one
// complete object. That is the contract the JSON writer keeps, so a test that
// parsed leniently would not be testing it.
func jsonLines(t *testing.T, stream string) []map[string]any {
	t.Helper()

	var out []map[string]any

	for line := range strings.SplitSeq(strings.TrimSpace(stream), "\n") {
		if line == "" {
			continue
		}

		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %q is not a JSON object: %v", line, err)
		}

		out = append(out, obj)
	}

	return out
}

// repoJSON renders one entry of a repository listing, with the four fields
// enumeration reads.
func repoJSON(owner, name string, archived, fork, private, hasIssues bool) string {
	return fmt.Sprintf(
		`{"name":%q,"owner":{"login":%q},"archived":%t,"fork":%t,"private":%t,"has_issues":%t}`,
		name, owner, archived, fork, private, hasIssues,
	)
}

// recorder records what the fake API saw. Repositories are read in parallel, so
// a test that appended to a plain slice from the handler would be a data race
// the -race detector finds on some runs and not others.
type recorder struct {
	mu       sync.Mutex
	requests []string
}

// record files one request as "METHOD path".
func (r *recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
}

// all returns what the fake API saw, as a copy.
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.requests...)
}

// matching returns the recorded requests containing substr.
func (r *recorder) matching(substr string) []string {
	var out []string

	for _, request := range r.all() {
		if strings.Contains(request, substr) {
			out = append(out, request)
		}
	}

	return out
}

// watch wraps a handler so every request through it is recorded.
func watch(next http.Handler) (http.Handler, *recorder) {
	log := &recorder{}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.record(r)

		next.ServeHTTP(w, r)
	}), log
}

// writeJSON is fmt.Fprint with the error dropped: a test server's response
// writer has nowhere useful to report a failed write to.
func writeJSON(w io.Writer, body string) {
	_, _ = fmt.Fprint(w, body)
}
