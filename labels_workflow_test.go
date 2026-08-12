package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// labelsWorkflowPath is the workflow that runs labelsync against this
// repository's own labels — the tool's first real user.
const labelsWorkflowPath = ".github/workflows/labels.yml"

// What the workflow promises, in the shape the test needs to read it. As with
// [releaseConfig], the fields left out are the ones nothing is asserted about:
// this stays a view of the promises rather than a second copy of the file.
type labelsWorkflow struct {
	On struct {
		PullRequest struct {
			Paths []string `yaml:"paths"`
		} `yaml:"pull_request"`
		Push struct {
			Branches []string `yaml:"branches"`
			Paths    []string `yaml:"paths"`
		} `yaml:"push"`
		Schedule []struct {
			Cron string `yaml:"cron"`
		} `yaml:"schedule"`
	} `yaml:"on"`

	Permissions map[string]string `yaml:"permissions"`

	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Env            map[string]string `yaml:"env"`
	Steps          []workflowStep    `yaml:"steps"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

func loadLabelsWorkflow(t *testing.T) labelsWorkflow {
	t.Helper()

	content, err := os.ReadFile(labelsWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", labelsWorkflowPath, err)
	}

	var wf labelsWorkflow
	if err := yaml.Unmarshal(content, &wf); err != nil {
		t.Fatalf("parse %s: %v", labelsWorkflowPath, err)
	}

	// One job, so that "the job" is a thing the assertions below can talk about
	// without naming it. Splitting the check and the apply into two jobs would be
	// two builds of the same binary for two steps that never both run.
	if len(wf.Jobs) != 1 {
		t.Fatalf("want exactly one job, got %d", len(wf.Jobs))
	}

	return wf
}

// job returns the workflow's only job, whatever it happens to be keyed under.
func (wf labelsWorkflow) job() workflowJob {
	for _, j := range wf.Jobs {
		return j
	}

	return workflowJob{}
}

// step finds the one step whose name contains want, case-insensitively. Steps
// are addressed by name rather than by index so that reordering them, or adding
// one, does not fail a test that has nothing to say about order.
func (wf labelsWorkflow) step(t *testing.T, want string) workflowStep {
	t.Helper()

	for _, s := range wf.job().Steps {
		if strings.Contains(strings.ToLower(s.Name), strings.ToLower(want)) {
			return s
		}
	}

	t.Fatalf("%s has no step named like %q", labelsWorkflowPath, want)

	return workflowStep{}
}

// The three triggers do three different jobs, and dropping any one leaves a gap
// nothing else covers: the pull-request run is the check, the push run applies
// what was just merged, and the schedule is the only trigger that ever sees a
// label somebody edited in the web UI — that produces no push at all.
func TestLabelsWorkflow_TriggersOnConfigChangesAndOnASchedule(t *testing.T) {
	wf := loadLabelsWorkflow(t)

	for _, paths := range [][]string{wf.On.PullRequest.Paths, wf.On.Push.Paths} {
		if !slices.Contains(paths, "labels.yml") {
			t.Errorf("paths = %v, want it to include %q", paths, "labels.yml")
		}

		// A change to the workflow is as much a reason to run it as a change to
		// the config it reads, and it is the only way a pull request editing the
		// workflow gets checked by it at all.
		if !slices.Contains(paths, labelsWorkflowPath) {
			t.Errorf("paths = %v, want it to include %q", paths, labelsWorkflowPath)
		}
	}

	if branches := wf.On.Push.Branches; !slices.Contains(branches, "main") {
		t.Errorf("on.push.branches = %v, want it to include %q", branches, "main")
	}

	if len(wf.On.Schedule) == 0 {
		t.Error("on.schedule is empty, so nothing ever corrects a label edited in the web UI")
	}
}

// The pull-request run is a check, not a preview. --dry-run writes nothing and
// exits 2 when the committed config and the live labels disagree, which fails the
// job — so a config change cannot merge while claiming to be already applied.
func TestLabelsWorkflow_PullRequestsGetADryRunCheck(t *testing.T) {
	drift := loadLabelsWorkflow(t).step(t, "drift")

	for _, want := range []string{"sync", "--dry-run", "--output=json"} {
		if !strings.Contains(drift.Run, want) {
			t.Errorf("the drift step runs %q, want it to contain %q", drift.Run, want)
		}
	}

	if !strings.Contains(drift.If, "github.event_name == 'pull_request'") {
		t.Errorf("the drift step is gated on %q, want it to run only on pull requests", drift.If)
	}
}

// The apply is the other half, and it must not be a second dry run: a workflow
// whose every step writes nothing looks green forever while the labels drift. It
// is also append-only — the scheduled job deleting labels unattended is the one
// outcome nobody would find out about until an issue had lost a label.
func TestLabelsWorkflow_MergesAndSchedulesApply(t *testing.T) {
	apply := loadLabelsWorkflow(t).step(t, "apply")

	if !strings.Contains(apply.Run, "sync") || !strings.Contains(apply.Run, "--output=json") {
		t.Errorf("the apply step runs %q, want it to sync with JSON output", apply.Run)
	}

	if strings.Contains(apply.Run, "--dry-run") {
		t.Errorf("the apply step runs %q, which writes nothing", apply.Run)
	}

	if strings.Contains(apply.Run, "prune") {
		t.Errorf("the apply step runs %q, want no prune: an unattended delete is never implicit", apply.Run)
	}

	if !strings.Contains(apply.If, "github.event_name != 'pull_request'") {
		t.Errorf("the apply step is gated on %q, want it to run on everything but a pull request", apply.If)
	}
}

// The token is the whole reason this workflow needs setting up deliberately. The
// GITHUB_TOKEN Actions injects is scoped to the repository the workflow runs in
// and cannot write labels anywhere else, so every run authenticates with the PAT
// secret — exported as GH_TOKEN, which the resolver reads before GITHUB_TOKEN so
// that an environment holding both cannot silently pick the useless one.
func TestLabelsWorkflow_AuthenticatesWithThePATSecret(t *testing.T) {
	wf := loadLabelsWorkflow(t)

	if got, want := wf.job().Env["GH_TOKEN"], "${{ secrets.LABELSYNC_TOKEN }}"; got != want {
		t.Errorf("env.GH_TOKEN = %q, want %q", got, want)
	}

	for _, step := range append([]workflowStep{{Name: "the job", Env: wf.job().Env}}, wf.job().Steps...) {
		if _, ok := step.Env["GITHUB_TOKEN"]; ok {
			t.Errorf("%s sets GITHUB_TOKEN, and a repository-scoped token cannot write labels elsewhere", step.Name)
		}

		// --token lands the credential in the command line, and therefore in the
		// log.
		if strings.Contains(step.Run, "--token") {
			t.Errorf("%s runs %q, which puts the token in the log", step.Name, step.Run)
		}
	}
}

// Two more things the job needs, both invisible when they regress: the automatic
// token is narrowed to the checkout it is there for, and the job is given long
// enough to finish. Writes are paced under GitHub's content-creation limit, so an
// apply is minutes rather than seconds, and a run killed by a short timeout
// leaves the labels half converged.
func TestLabelsWorkflow_NarrowsTheAutomaticTokenAndAllowsForAPacedRun(t *testing.T) {
	wf := loadLabelsWorkflow(t)

	if got, want := wf.Permissions["contents"], "read"; got != want {
		t.Errorf("permissions.contents = %q, want %q", got, want)
	}

	if len(wf.Permissions) != 1 {
		t.Errorf("permissions = %v, want nothing but contents: read", wf.Permissions)
	}

	if got := wf.job().TimeoutMinutes; got < 30 {
		t.Errorf("timeout-minutes = %d, want at least 30 for a rate-limited apply", got)
	}
}
