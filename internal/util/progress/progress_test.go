package progress_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/specsnl/labelsync/internal/util/output"
	"github.com/specsnl/labelsync/internal/util/progress"
)

// erased is what blanks the row: a carriage return and "erase to end of line".
const erased = "\r\x1b[K"

func wide() int { return 200 }

// TestNewDrawsNothingWithoutATerminal is the TTY-off half: a buffer is not a
// terminal, so every rendering New can pick for it writes nothing at all — no
// carriage return, no escape sequence — whatever it is told.
func TestNewDrawsNothingWithoutATerminal(t *testing.T) {
	tests := []struct {
		name   string
		format output.Format
		debug  bool
	}{
		{name: "pretty", format: output.FormatPretty},
		{name: "json", format: output.FormatJSON},
		{name: "debug", format: output.FormatPretty, debug: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer

			p := progress.New(&stderr, tt.format, tt.debug)

			p.Enumerating(1, 2, 30)
			p.Reading(3, 4)
			p.Applying(progress.Apply{Repo: "specsnl/x", Repos: 1, ReposTotal: 2, Writes: 3, WritesTotal: 4})
			p.Pause()
			p.Resume()
			p.Done()

			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
		})
	}
}

// TestLiveRendersEachPhase is the TTY-on half: each phase rewrites the row from
// column zero, so the terminal only ever shows the latest line.
func TestLiveRendersEachPhase(t *testing.T) {
	tests := []struct {
		name string
		draw func(p progress.Reporter)
		want string
	}{
		{
			name: "enumerating",
			draw: func(p progress.Reporter) { p.Enumerating(2, 4, 230) },
			want: "enumerating… 2/4 groups · 230 repositories",
		},
		{
			name: "enumerating singular",
			draw: func(p progress.Reporter) { p.Enumerating(0, 1, 1) },
			want: "enumerating… 0/1 group · 1 repository",
		},
		{
			name: "reading",
			draw: func(p progress.Reporter) { p.Reading(23, 65) },
			want: "reading labels… 23/65 repositories",
		},
		{
			name: "applying",
			draw: func(p progress.Reporter) {
				p.Applying(progress.Apply{
					Repo: "specsnl/specsops-ansible", Repos: 12, ReposTotal: 65, Writes: 48, WritesTotal: 179,
				})
			},
			want: "applying… 12/65 repositories · 48/179 writes · specsnl/specsops-ansible",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer

			tt.draw(progress.NewLive(&stderr, wide))

			if got := stderr.String(); got != erased+tt.want {
				t.Errorf("stderr = %q, want %q", got, erased+tt.want)
			}
		})
	}
}

// TestLiveDoneErasesTheLine is what keeps the final output looking the way it
// did before progress existed: whatever comes next starts on a blank row.
func TestLiveDoneErasesTheLine(t *testing.T) {
	var stderr bytes.Buffer

	p := progress.NewLive(&stderr, wide)

	p.Reading(1, 2)
	p.Done()

	if got, want := stderr.String(), erased+"reading labels… 1/2 repositories"+erased; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}

	// A second Done has nothing on the row to erase, and writes nothing.
	stderr.Reset()
	p.Done()

	if stderr.Len() != 0 {
		t.Errorf("a Done with nothing drawn wrote %q", stderr.String())
	}
}

// TestLivePauseHoldsTheLineUntilTheLastResume is the countdown's half of the
// row. While a wait is shown the progress line stays down, even as the phase
// advances underneath it, and comes back with the latest text, not the text it
// had when it was paused.
func TestLivePauseHoldsTheLineUntilTheLastResume(t *testing.T) {
	var stderr bytes.Buffer

	p := progress.NewLive(&stderr, wide)

	p.Reading(1, 3)
	stderr.Reset()

	p.Pause()
	p.Pause()

	if got := stderr.String(); got != erased {
		t.Fatalf("Pause wrote %q, want the line erased once", got)
	}

	stderr.Reset()

	p.Reading(2, 3)
	p.Resume()

	if stderr.Len() != 0 {
		t.Fatalf("drew %q with a pause still outstanding", stderr.String())
	}

	p.Resume()

	if got, want := stderr.String(), erased+"reading labels… 2/3 repositories"; got != want {
		t.Errorf("last Resume wrote %q, want %q", got, want)
	}
}

// TestLiveResumeAfterDoneDrawsNothing covers a wait that outlives its phase: the
// phase ended while the countdown held the row, so there is nothing to bring
// back.
func TestLiveResumeAfterDoneDrawsNothing(t *testing.T) {
	var stderr bytes.Buffer

	p := progress.NewLive(&stderr, wide)

	p.Reading(1, 1)
	p.Pause()
	p.Done()
	stderr.Reset()

	p.Resume()

	if stderr.Len() != 0 {
		t.Errorf("Resume after Done wrote %q", stderr.String())
	}
}

// TestLiveFitsTheTerminal pins the cut. A line wider than the terminal wraps,
// and the carriage return then only rewinds to the start of the last row, which
// leaves every redraw stacked under the one before.
func TestLiveFitsTheTerminal(t *testing.T) {
	var stderr bytes.Buffer

	p := progress.NewLive(&stderr, func() int { return 20 })

	p.Reading(23, 65)

	line := strings.TrimPrefix(stderr.String(), erased)

	if got, want := line, "reading labels… 23…"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}

	if n := len([]rune(line)); n != 19 {
		t.Errorf("line is %d columns, want 19: one short of the terminal", n)
	}
}

// TestLiveIsSafeForConcurrentUse is for the race detector: the reads report
// from as many goroutines as --concurrency allows.
func TestLiveIsSafeForConcurrentUse(t *testing.T) {
	var (
		stderr bytes.Buffer
		wg     sync.WaitGroup
	)

	p := progress.NewLive(&stderr, wide)

	for i := range 8 {
		wg.Go(func() {
			p.Reading(i, 8)
			p.Pause()
			p.Resume()
		})
	}

	wg.Wait()
	p.Done()

	if !strings.HasSuffix(stderr.String(), erased) {
		t.Errorf("stderr = %q, want it to end on an erased row", stderr.String())
	}
}
