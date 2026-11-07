// Package progresstest is a [progress.Reporter] that writes down what it was
// told, for the tests of the packages that report progress.
package progresstest

import (
	"fmt"
	"slices"
	"sync"

	"github.com/specsnl/labelsync/internal/util/progress"
)

// Recorder records every call as one line, in the order the calls arrived.
type Recorder struct {
	mu    sync.Mutex
	calls []string
}

var _ progress.Reporter = (*Recorder)(nil)

// Enumerating implements [progress.Reporter].
func (r *Recorder) Enumerating(groupsDone, groupsTotal, repos int) {
	r.add(fmt.Sprintf("enumerating %d/%d %d", groupsDone, groupsTotal, repos))
}

// Reading implements [progress.Reporter].
func (r *Recorder) Reading(done, total int) {
	r.add(fmt.Sprintf("reading %d/%d", done, total))
}

// Applying implements [progress.Reporter].
func (r *Recorder) Applying(a progress.Apply) {
	r.add(fmt.Sprintf("applying %d/%d %d/%d %s", a.Repos, a.ReposTotal, a.Writes, a.WritesTotal, a.Repo))
}

// Done implements [progress.Reporter].
func (r *Recorder) Done() { r.add("done") }

// Pause implements [progress.Reporter].
func (r *Recorder) Pause() { r.add("pause") }

// Resume implements [progress.Reporter].
func (r *Recorder) Resume() { r.add("resume") }

// Calls returns a copy of everything recorded so far.
func (r *Recorder) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.calls)
}

// Last is the most recent call, or "" when there has been none.
func (r *Recorder) Last() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.calls) == 0 {
		return ""
	}

	return r.calls[len(r.calls)-1]
}

func (r *Recorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, call)
}
