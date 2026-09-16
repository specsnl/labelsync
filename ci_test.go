package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ciWorkflowPath is the workflow whose `image` job is the only thing between a
// broken Dockerfile and a tag being cut with it.
const ciWorkflowPath = ".github/workflows/ci.yml"

// batsPath is the acceptance script the guard runs, and the one `task
// image:smoke` runs against the same images locally.
const batsPath = "test/image.bats"

// The CI workflow, in the shape these tests read it. As elsewhere, the fields
// left out are the ones nothing is asserted about.
type ciWorkflow struct {
	Jobs map[string]struct {
		RunsOn   string `yaml:"runs-on"`
		Strategy struct {
			Matrix struct {
				Target []string       `yaml:"target"`
				Runner []matrixRunner `yaml:"runner"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []ciStep `yaml:"steps"`
	} `yaml:"jobs"`
}

type ciStep struct {
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

func loadCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()

	content, err := os.ReadFile(ciWorkflowPath)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflowPath, err)
	}

	var workflow ciWorkflow
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatalf("parse %s: %v", ciWorkflowPath, err)
	}

	if _, ok := workflow.Jobs["image"]; !ok {
		t.Fatalf("%s has no `image` job; jobs are %v", ciWorkflowPath, slices.Sorted(maps.Keys(workflow.Jobs)))
	}

	return workflow
}

// step returns the first step of the image job whose `uses:` or `run:` contains
// needle.
func (w ciWorkflow) step(t *testing.T, needle string) ciStep {
	t.Helper()

	for _, step := range w.Jobs["image"].Steps {
		if strings.Contains(step.Uses, needle) || strings.Contains(step.Run, needle) {
			return step
		}
	}

	t.Fatalf("the image job has no step naming %q", needle)

	return ciStep{}
}

// A stage the guard does not build is a stage whose first run is a release. Both
// published stages are checked, on a runner of their own architecture, because
// the point is to *run* the binary: a build that only compiles the foreign
// architecture proves nothing about the image it produced.
func TestCI_ImageGuardCoversEveryPublishedStage(t *testing.T) {
	job := loadCIWorkflow(t).Jobs["image"]

	want := slices.Sorted(maps.Values(publishedTargets))

	if got := slices.Sorted(slices.Values(job.Strategy.Matrix.Target)); !slices.Equal(got, want) {
		t.Errorf("the guard builds targets %v, want %v — the same stages the release publishes", got, want)
	}

	runners := map[string]string{
		"linux/amd64": "ubuntu-24.04",
		"linux/arm64": "ubuntu-24.04-arm",
	}

	for _, runner := range job.Strategy.Matrix.Runner {
		native, ok := runners[runner.Platform]
		if !ok {
			t.Errorf("the guard builds unexpected platform %q", runner.Platform)

			continue
		}

		if runner.OS != native {
			t.Errorf("the guard builds %s on %q, want %q — a foreign image cannot be run",
				runner.Platform, runner.OS, native)
		}

		delete(runners, runner.Platform)
	}

	for platform := range runners {
		t.Errorf("the guard does not build %s", platform)
	}
}

// The version build arg is the one input a rename fails silently on, and the
// guard exists to make that loud: it builds with a version no fallback could
// produce and the script asserts the binary reports exactly that. The two have
// to agree, or the guard fails on every run and gets deleted.
func TestCI_ImageGuardAssertsTheInjectedVersion(t *testing.T) {
	workflow := loadCIWorkflow(t)

	buildArgs := fmt.Sprint(workflow.step(t, "specsnl/github-actions/build-image").With["build-args"])

	version, ok := strings.CutPrefix(strings.TrimSpace(buildArgs), versionBuildArg+"=")
	if !ok {
		t.Fatalf("the build step passes build-args %q, want them to start with %s=", buildArgs, versionBuildArg)
	}

	smoke := workflow.step(t, batsPath)

	if got := smoke.Env["EXPECTED_VERSION"]; got != version {
		t.Errorf("the smoke step expects version %q, but the image is built with %q", got, version)
	}

	if smoke.Env["TARGET"] == "" {
		t.Error("the smoke step passes no TARGET, so the script cannot tell the scratch image from the debian one")
	}
}

// The script the guard runs is the same file `task image:smoke` runs, so a
// rename that only lands in one of them leaves the other calling a path that
// does not exist — which CI reports as a failing job and a developer reports as
// a broken task.
func TestCI_ImageGuardRunsTheCheckedInScript(t *testing.T) {
	if _, err := os.Stat(batsPath); err != nil {
		t.Fatalf("stat %s: %v", batsPath, err)
	}

	taskfile, err := os.ReadFile("taskfiles/Taskfile.image.yml")
	if err != nil {
		t.Fatalf("read the image taskfile: %v", err)
	}

	if !strings.Contains(string(taskfile), batsPath) {
		t.Errorf("task image:smoke does not run %s, so the local checks and CI are two different suites", batsPath)
	}
}
