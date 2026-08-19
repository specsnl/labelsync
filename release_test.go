package main

import (
	"os"
	"slices"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// What the release config promises, in the shape the test needs to read it. The
// fields the test does not assert are left out: yaml.Unmarshal ignores the rest,
// so this stays a view of the promises rather than a second copy of the file.
type releaseConfig struct {
	Builds []struct {
		Binary string   `yaml:"binary"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
		Env    []string `yaml:"env"`
	} `yaml:"builds"`
	HomebrewCasks []struct {
		Name       string   `yaml:"name"`
		Binaries   []string `yaml:"binaries"`
		Repository struct {
			Owner string `yaml:"owner"`
			Name  string `yaml:"name"`
			Token string `yaml:"token"`
		} `yaml:"repository"`
		Hooks struct {
			Post struct {
				Install string `yaml:"install"`
			} `yaml:"post"`
		} `yaml:"hooks"`
	} `yaml:"homebrew_casks"`
}

func loadReleaseConfig(t *testing.T) releaseConfig {
	t.Helper()

	content, err := os.ReadFile(".goreleaser.yml")
	if err != nil {
		t.Fatalf("read .goreleaser.yml: %v", err)
	}

	var cfg releaseConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		t.Fatalf("parse .goreleaser.yml: %v", err)
	}

	if len(cfg.Builds) != 1 {
		t.Fatalf("want exactly one build, got %d", len(cfg.Builds))
	}

	return cfg
}

// The platform matrix is what the README and the releases page promise: Linux and
// macOS, amd64 and arm64. Dropping one is invisible in review — the release still
// succeeds, it just quietly stops shipping a platform someone installed from.
func TestRelease_CoversEveryPromisedPlatform(t *testing.T) {
	build := loadReleaseConfig(t).Builds[0]

	for _, goos := range []string{"linux", "darwin"} {
		if !slices.Contains(build.Goos, goos) {
			t.Errorf("goos = %v, want it to include %q", build.Goos, goos)
		}
	}

	for _, goarch := range []string{"amd64", "arm64"} {
		if !slices.Contains(build.Goarch, goarch) {
			t.Errorf("goarch = %v, want it to include %q", build.Goarch, goarch)
		}
	}
}

// A cgo build links against the host's libc, so it would ship a binary that runs
// on the runner and not necessarily on the machine that downloaded it.
func TestRelease_BinariesAreStaticallyLinked(t *testing.T) {
	build := loadReleaseConfig(t).Builds[0]

	if !slices.Contains(build.Env, "CGO_ENABLED=0") {
		t.Errorf("build env = %v, want CGO_ENABLED=0", build.Env)
	}
}

// The tap lives in another repository, so the cask needs a token that
// secrets.GITHUB_TOKEN cannot be. Nothing fails until the last step of a real
// release if this stops naming the secret the workflow passes in.
func TestRelease_CaskPublishesToTheTapWithItsOwnToken(t *testing.T) {
	casks := loadReleaseConfig(t).HomebrewCasks
	if len(casks) != 1 {
		t.Fatalf("want exactly one cask, got %d", len(casks))
	}

	cask := casks[0]

	if got := cask.Repository.Owner + "/" + cask.Repository.Name; got != "specsnl/homebrew-tap" {
		t.Errorf("tap = %q, want %q", got, "specsnl/homebrew-tap")
	}

	const secret = "HOMEBREW_TAP_GITHUB_TOKEN"

	if !strings.Contains(cask.Repository.Token, secret) {
		t.Errorf("cask token = %q, want it to read .Env.%s", cask.Repository.Token, secret)
	}

	workflow, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read the release workflow: %v", err)
	}

	if !strings.Contains(string(workflow), secret) {
		t.Errorf("the release workflow does not pass %s; the cask step would fail after the release is created", secret)
	}
}

// A pre-release tag and a stable tag must write different files in the tap, or an
// rc overwrites the one cask `brew upgrade` follows and hands everyone a release
// candidate until the stable ships. The name templates on .Prerelease, which is
// `rc.1` for v0.1.0-rc.1 and empty for v0.1.0.
func TestRelease_PrereleasesGetTheirOwnCask(t *testing.T) {
	casks := loadReleaseConfig(t).HomebrewCasks
	if len(casks) != 1 {
		t.Fatalf("want exactly one cask, got %d", len(casks))
	}

	tmpl, err := template.New("cask").Parse(casks[0].Name)
	if err != nil {
		t.Fatalf("parse the cask name as a template: %v", err)
	}

	for _, tc := range []struct {
		tag        string
		prerelease string
		want       string
	}{
		{tag: "v0.9.9", prerelease: "", want: "labelsync"},
		{tag: "v0.9.9-rc.1", prerelease: "rc.1", want: "labelsync@rc"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			var got strings.Builder
			if err := tmpl.Execute(&got, struct{ Prerelease string }{tc.prerelease}); err != nil {
				t.Fatalf("render the cask name: %v", err)
			}

			if got.String() != tc.want {
				t.Errorf("cask name for %s = %q, want %q", tc.tag, got.String(), tc.want)
			}
		})
	}
}

// goreleaser defaults `binaries` to the cask *name*, so templating the name alone
// emits `binary "labelsync@rc"` while the archive holds `labelsync` — a cask that
// installs nothing, and nothing goes red until someone tries the rc.
func TestRelease_CaskNamesTheBinaryInTheArchive(t *testing.T) {
	cfg := loadReleaseConfig(t)

	casks := cfg.HomebrewCasks
	if len(casks) != 1 {
		t.Fatalf("want exactly one cask, got %d", len(casks))
	}

	want := []string{cfg.Builds[0].Binary}

	if !slices.Equal(casks[0].Binaries, want) {
		t.Errorf("cask binaries = %v, want %v — what the archive actually contains", casks[0].Binaries, want)
	}
}

// macOS quarantines an unsigned binary downloaded over HTTP, so without the hook
// `brew install` succeeds and the very next command is refused by Gatekeeper —
// a failure that surfaces nowhere near its cause.
func TestRelease_CaskClearsTheQuarantineAttribute(t *testing.T) {
	casks := loadReleaseConfig(t).HomebrewCasks
	if len(casks) != 1 {
		t.Fatalf("want exactly one cask, got %d", len(casks))
	}

	install := casks[0].Hooks.Post.Install

	for _, want := range []string{"xattr", "-dr", "com.apple.quarantine"} {
		if !strings.Contains(install, want) {
			t.Errorf("the post-install hook does not mention %q:\n%s", want, install)
		}
	}
}
