package cmd

import (
	"testing"
	"time"

	"github.com/specsnl/labelsync/internal/github/ratelimit"
	"github.com/specsnl/labelsync/internal/util/progress/progresstest"
)

// countdown is a ratelimit.Reporter that checks, at the moment it is asked to
// draw or undraw, whether the progress line is out of its way.
type countdown struct {
	t        *testing.T
	progress *progresstest.Recorder
}

func (countdown) Interval() time.Duration { return time.Second }

func (c countdown) Start(ratelimit.Wait) {
	if got := c.progress.Last(); got != "pause" {
		c.t.Errorf("countdown started after %q, want the progress line paused first", got)
	}
}

func (c countdown) Tick(ratelimit.Wait, time.Duration) {}

func (c countdown) Done(ratelimit.Wait) {
	if got := c.progress.Last(); got == "resume" {
		c.t.Error("progress resumed before the countdown cleared its line")
	}
}

// TestPausingKeepsTheCountdownAndProgressApart is the one row both draw on: the
// progress line comes down before the countdown draws, and goes back up only
// after the countdown has cleared itself.
func TestPausingKeepsTheCountdownAndProgressApart(t *testing.T) {
	recorder := &progresstest.Recorder{}

	reporter := pausing{Reporter: countdown{t: t, progress: recorder}, progress: recorder}

	wait := ratelimit.Wait{Kind: ratelimit.KindSecondary, Total: time.Minute}

	reporter.Start(wait)
	reporter.Tick(wait, time.Minute)
	reporter.Done(wait)

	if got := recorder.Calls(); len(got) != 2 || got[0] != "pause" || got[1] != "resume" {
		t.Errorf("progress = %v, want [pause resume]", got)
	}
}
