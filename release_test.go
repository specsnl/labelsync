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

// What the release config promises, in the shape the test needs to read it. The
// fields the test does not assert are left out: yaml.Unmarshal ignores the rest,
// so this stays a view of the promises rather than a second copy of the file.
type releaseConfig struct {
	Builds []struct {
		Binary  string   `yaml:"binary"`
		Goos    []string `yaml:"goos"`
		Goarch  []string `yaml:"goarch"`
		Env     []string `yaml:"env"`
		Flags   []string `yaml:"flags"`
		Ldflags []string `yaml:"ldflags"`
	} `yaml:"builds"`
	HomebrewCasks []releaseCask `yaml:"homebrew_casks"`
}

// SkipUpload is `any` because goreleaser types it as `string | boolean`: the
// value in use is the string `auto`, and what the tests read is whether the key
// is there at all, which a typed field could not express.
type releaseCask struct {
	Name       string   `yaml:"name"`
	Binaries   []string `yaml:"binaries"`
	SkipUpload any      `yaml:"skip_upload"`
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

// The two cask entries by name. Everything except the name and `skip_upload`
// comes from a YAML anchor on the rc entry, so the assertions below read both
// entries back rather than trusting the merge to have carried anything.
func loadCasks(t *testing.T) map[string]releaseCask {
	t.Helper()

	byName := map[string]releaseCask{}

	for _, cask := range loadReleaseConfig(t).HomebrewCasks {
		byName[cask.Name] = cask
	}

	for _, name := range []string{caskStable, caskRC} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("no %q cask entry; the entries are %v", name, slices.Sorted(maps.Keys(byName)))
		}
	}

	if len(byName) != 2 {
		t.Fatalf("want exactly the two cask entries, got %v", slices.Sorted(maps.Keys(byName)))
	}

	return byName
}

const (
	caskStable = "labelsync"
	caskRC     = "labelsync@rc"
)

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
	const secret = "HOMEBREW_TAP_GITHUB_TOKEN"

	for name, cask := range loadCasks(t) {
		if got := cask.Repository.Owner + "/" + cask.Repository.Name; got != "specsnl/homebrew-tap" {
			t.Errorf("%s tap = %q, want %q", name, got, "specsnl/homebrew-tap")
		}

		if !strings.Contains(cask.Repository.Token, secret) {
			t.Errorf("%s token = %q, want it to read .Env.%s", name, cask.Repository.Token, secret)
		}
	}

	workflow, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read the release workflow: %v", err)
	}

	if !strings.Contains(string(workflow), secret) {
		t.Errorf("the release workflow does not pass %s; the cask step would fail after the release is created", secret)
	}
}

// Which channel each entry publishes to is `skip_upload` and nothing else, and
// it is invisible until a real tag: `auto` on the stable entry skips it for a
// tag with a semver pre-release component, while the rc entry carries no
// `skip_upload` at all and therefore publishes on every tag, stable ones
// included. Drop the `auto` and an rc overwrites the cask `brew upgrade`
// follows; add one to the rc entry and someone who opted into `@rc` is stranded
// on a candidate for the whole gap between series.
func TestRelease_StableSkipsPrereleasesAndRcTakesEveryTag(t *testing.T) {
	casks := loadCasks(t)

	if got := casks[caskStable].SkipUpload; got != "auto" {
		t.Errorf("%s skip_upload = %v, want %q — otherwise a pre-release tag overwrites the stable cask",
			caskStable, got, "auto")
	}

	if got := casks[caskRC].SkipUpload; got != nil {
		t.Errorf("%s skip_upload = %v, want it unset — the rc cask tracks the leading edge, stable tags included",
			caskRC, got)
	}
}

// goreleaser defaults `binaries` to the cask *name*, so the rc entry would
// otherwise emit `binary "labelsync@rc"` while the archive holds `labelsync` — a
// cask that installs nothing, and nothing goes red until someone tries the rc.
func TestRelease_CaskNamesTheBinaryInTheArchive(t *testing.T) {
	cfg := loadReleaseConfig(t)
	want := []string{cfg.Builds[0].Binary}

	for name, cask := range loadCasks(t) {
		if !slices.Equal(cask.Binaries, want) {
			t.Errorf("%s binaries = %v, want %v — what the archive actually contains", name, cask.Binaries, want)
		}
	}
}

// macOS quarantines an unsigned binary downloaded over HTTP, so without the hook
// `brew install` succeeds and the very next command is refused by Gatekeeper —
// a failure that surfaces nowhere near its cause.
func TestRelease_CaskClearsTheQuarantineAttribute(t *testing.T) {
	for name, cask := range loadCasks(t) {
		install := cask.Hooks.Post.Install

		for _, want := range []string{"xattr", "-dr", "com.apple.quarantine"} {
			if !strings.Contains(install, want) {
				t.Errorf("the %s post-install hook does not mention %q:\n%s", name, want, install)
			}
		}
	}
}

// The release workflow, in the shape these tests read it. `on:` is left out
// deliberately: yaml.v3 parses the bare key as the boolean true, and nothing
// here needs the trigger.
type releaseWorkflow struct {
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]struct {
		Permissions map[string]string `yaml:"permissions"`
		Strategy    struct {
			Matrix struct {
				Include []struct {
					Package string `yaml:"package"`
					Target  string `yaml:"target"`
				} `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Uses string         `yaml:"uses"`
			With map[string]any `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func loadReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()

	content, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatalf("read the release workflow: %v", err)
	}

	var workflow releaseWorkflow
	if err := yaml.Unmarshal(content, &workflow); err != nil {
		t.Fatalf("parse the release workflow: %v", err)
	}

	if _, ok := workflow.Jobs["images"]; !ok {
		t.Fatalf("the release workflow has no `images` job; jobs are %v", slices.Sorted(maps.Keys(workflow.Jobs)))
	}

	return workflow
}

// imagesStepWith returns the `with:` value of the first step of the images job
// whose `uses:` names action, as a string — the block mixes strings and
// booleans, so everything is read back through fmt.Sprint rather than typed per
// key.
func imagesStepWith(t *testing.T, action, key string) string {
	t.Helper()

	for _, step := range loadReleaseWorkflow(t).Jobs["images"].Steps {
		if !strings.Contains(step.Uses, action) {
			continue
		}

		value, ok := step.With[key]
		if !ok {
			t.Fatalf("the %s step has no %q; it has %v", action, key, slices.Sorted(maps.Keys(step.With)))
		}

		return fmt.Sprint(value)
	}

	t.Fatalf("the images job has no step using %s", action)

	return ""
}

// Two packages, from two stages of one Dockerfile. The names are the addresses
// consumers pin to and the stages are how `target:` selects a runtime, so a
// rename on either side of that pairing silently publishes the wrong image —
// or, if the stage no longer exists, publishes nothing until the release fails.
func TestRelease_ImagesJobPublishesBothPackages(t *testing.T) {
	include := loadReleaseWorkflow(t).Jobs["images"].Strategy.Matrix.Include

	want := map[string]string{
		"ghcr.io/specsnl/labelsync":        "binary",
		"ghcr.io/specsnl/labelsync/debian": "debian",
	}

	if len(include) != len(want) {
		t.Fatalf("the images matrix has %d entries, want %d", len(include), len(want))
	}

	stages := dockerfileStages(t)

	for _, entry := range include {
		target, ok := want[entry.Package]
		if !ok {
			t.Errorf("unexpected package %q in the images matrix", entry.Package)

			continue
		}

		if entry.Target != target {
			t.Errorf("%s builds target %q, want %q", entry.Package, entry.Target, target)
		}

		if _, ok := stages[entry.Target]; !ok {
			t.Errorf("the Dockerfile has no %q stage for %s; stages are %v",
				entry.Target, entry.Package, slices.Sorted(maps.Keys(stages)))
		}

		delete(want, entry.Package)
	}

	for pkg := range want {
		t.Errorf("the images matrix does not publish %s", pkg)
	}
}

// Both platforms in one push, so every tag of one release resolves to one
// manifest digest. Dropping a platform is invisible in review: the release still
// succeeds, and an arm64 runner pulling it fails at `docker run` instead.
func TestRelease_ImagesCoverBothPlatforms(t *testing.T) {
	platforms := imagesStepWith(t, "docker/build-push-action", "platforms")

	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		if !strings.Contains(platforms, platform) {
			t.Errorf("platforms = %q, want it to include %q", platforms, platform)
		}
	}

	if push := imagesStepWith(t, "docker/build-push-action", "push"); push != "true" {
		t.Errorf("push = %q, want %q — the job would build both images and publish neither", push, "true")
	}
}

// Pushing to GHCR needs `packages: write`, which the workflow-level block does
// not grant: it is read-only so each job asks for its own write scope. Without
// this the whole release goes green and the images fail at the push.
func TestRelease_ImagesJobCanWritePackages(t *testing.T) {
	workflow := loadReleaseWorkflow(t)

	if got := workflow.Jobs["images"].Permissions["packages"]; got != "write" {
		t.Errorf("the images job has packages: %q, want %q", got, "write")
	}

	if got := workflow.Jobs["release"].Permissions["contents"]; got != "write" {
		t.Errorf("the release job has contents: %q, want %q", got, "write")
	}
}

// The tag policy is the whole contract a consumer pins against, and every part
// of it is one line of config that a plausible-looking edit can drop. A missing
// {{major}}.{{minor}} leaves early consumers pinned to a patch forever; a
// missing v0. guard promises stability across the releases most likely to break;
// a `latest=true` flavour would hand `docker run …:latest` a release candidate.
func TestRelease_ImageTagsFollowThePolicy(t *testing.T) {
	tags := imagesStepWith(t, "docker/metadata-action", "tags")

	for _, want := range []string{
		"type=semver,pattern={{version}}",
		"type=semver,pattern={{major}}.{{minor}}",
		"type=semver,pattern={{major}},enable=",
	} {
		if !strings.Contains(tags, want) {
			t.Errorf("the tag list does not contain %q:\n%s", want, tags)
		}
	}

	// No `v` prefix anywhere: `labelsync version` prints the tag without one.
	if strings.Contains(tags, "prefix=v") || strings.Contains(tags, "v{{version}}") {
		t.Errorf("the tag list prefixes tags with a v, which the version command never prints:\n%s", tags)
	}

	if !strings.Contains(tags, "!startsWith(github.ref, 'refs/tags/v0.')") {
		t.Errorf("the bare {{major}} is not guarded against 0.x, so a 0.x release would publish a :0 tag:\n%s", tags)
	}

	if flavor := imagesStepWith(t, "docker/metadata-action", "flavor"); !strings.Contains(flavor, "latest=auto") {
		t.Errorf("flavor = %q, want it to set latest=auto — a prerelease must not move :latest", flavor)
	}
}

// GHCR links a package to this repository through org.opencontainers.image.source,
// and an unlinked package inherits neither the repository's visibility nor its
// permissions — so it stays private, and off the Packages sidebar, however many
// releases push to it.
func TestRelease_ImagesCarryTheOCILabels(t *testing.T) {
	for _, key := range []string{"labels", "annotations"} {
		value := imagesStepWith(t, "docker/build-push-action", key)

		if !strings.Contains(value, "steps.meta.outputs."+key) {
			t.Errorf("%s = %q, want it to come from the metadata action, which emits image.source", key, value)
		}
	}
}

// One binary, two build paths: goreleaser for the archives and the cask, the
// Dockerfile for the images. The same source compiled with different flags
// produces images that differ from the tarball in ways nothing reports.
func TestRelease_DockerfileBuildFlagsMatchGoreleaser(t *testing.T) {
	build := loadReleaseConfig(t).Builds[0]
	dockerfile := readDockerfile(t)

	// -tags=netgo and -tags netgo are the same flag spelled two ways, so the
	// comparison is on the canonical form rather than on the literal text.
	canonical := strings.ReplaceAll(dockerfile, "-tags netgo", "-tags=netgo")

	for _, flag := range build.Flags {
		if !strings.Contains(canonical, strings.ReplaceAll(flag, " ", "=")) {
			t.Errorf("the Dockerfile does not pass %q, which .goreleaser.yml does", flag)
		}
	}

	if !slices.Contains(build.Env, "CGO_ENABLED=0") || !strings.Contains(dockerfile, "CGO_ENABLED=0") {
		t.Errorf("both build paths must set CGO_ENABLED=0; goreleaser env = %v", build.Env)
	}

	for _, ldflag := range build.Ldflags {
		if strings.HasPrefix(ldflag, "-X ") {
			// The version symbol, without the value each path substitutes into
			// it: goreleaser templates {{ .Version }}, the Dockerfile takes an
			// ARG. What has to agree is the symbol they inject into.
			symbol := strings.TrimPrefix(strings.SplitN(ldflag, "=", 2)[0], "-X ")

			module, path, ok := strings.Cut(symbol, "/internal/")
			if !ok {
				t.Fatalf("-X %q does not name a symbol under internal/", symbol)
			}

			// The Dockerfile spells the module as ${GO_MODULE}, defaulted by an
			// ARG — so the ARG's default is what has to match goreleaser.
			if !strings.Contains(dockerfile, "ARG GO_MODULE="+module) {
				t.Errorf("the Dockerfile's GO_MODULE arg does not default to %q", module)
			}

			if !strings.Contains(dockerfile, "${GO_MODULE}/internal/"+path) {
				t.Errorf("the Dockerfile does not inject the version into %q", symbol)
			}

			continue
		}

		if !strings.Contains(dockerfile, ldflag) {
			t.Errorf("the Dockerfile does not pass the ldflag %q, which .goreleaser.yml does", ldflag)
		}
	}
}

// An arm64 image needs no emulation at all, but only while the builder is pinned
// to the platform doing the building and takes its GOOS/GOARCH from the platform
// being built for. Unpin either and the arm64 leg runs apt-get and the whole
// compile under QEMU, which is slow enough to look like a hung release.
func TestRelease_ImagesCrossCompileRatherThanEmulate(t *testing.T) {
	dockerfile := readDockerfile(t)

	if !strings.Contains(dockerfile, "FROM --platform=$BUILDPLATFORM golang:") {
		t.Error("the builder is not pinned to $BUILDPLATFORM, so a multi-platform build would compile under emulation")
	}

	for _, want := range []string{"GOOS=${GOOS:-$TARGETOS}", "GOARCH=${GOARCH:-$TARGETARCH}"} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("the build stage does not compile with %s, so the binary need not match the image's platform", want)
		}
	}
}

// The one thing labelsync does is talk to api.github.com over TLS, and a
// runtime stage without the bundle fails every request with an x509 error —
// which the version command, and therefore any smoke test that runs it, still
// passes. The trailing slash carries the whole thing: without it the bundle
// lands as a file named /etc/ssl/certs and Go finds nothing.
func TestRelease_RuntimeImagesCarryTheCertBundle(t *testing.T) {
	stages := dockerfileStages(t)

	for _, stage := range []string{"binary", "debian"} {
		body := strings.Join(stages[stage], "\n")

		if !strings.Contains(body, "COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/") {
			t.Errorf("the %q stage does not copy the CA bundle into /etc/ssl/certs/:\n%s", stage, body)
		}
	}
}

// A container that only reads a config file and writes a best-effort cache has
// no reason to be root, and a published image is where that stops being
// theoretical: a bind-mounted config would be written back as root.
func TestRelease_RuntimeImagesDoNotRunAsRoot(t *testing.T) {
	stages := dockerfileStages(t)

	for _, stage := range []string{"binary", "debian"} {
		body := strings.Join(stages[stage], "\n")

		if !strings.Contains(body, "USER 65534:65534") {
			t.Errorf("the %q stage sets no non-root USER, so the published image runs as root:\n%s", stage, body)
		}

		if !strings.Contains(body, "ENTRYPOINT") {
			t.Errorf("the %q stage has no ENTRYPOINT, so `docker run … sync` would replace the binary rather than pass an argument to it", stage)
		}
	}
}

func readDockerfile(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatalf("read the Dockerfile: %v", err)
	}

	return string(content)
}

// dockerfileStages maps each named stage to the lines of its body. Comments and
// blank lines are kept: they belong to the stage they sit in, and an assertion
// that prints a body reads better with them.
func dockerfileStages(t *testing.T) map[string][]string {
	t.Helper()

	stages := map[string][]string{}
	current := ""

	for line := range strings.Lines(readDockerfile(t)) {
		fields := strings.Fields(line)

		if len(fields) > 0 && strings.EqualFold(fields[0], "FROM") {
			current = ""

			// FROM [--platform=…] <image> [AS <name>] — the name is the last
			// field, and only when the one before it is AS.
			if len(fields) >= 2 && strings.EqualFold(fields[len(fields)-2], "AS") {
				current = fields[len(fields)-1]
				stages[current] = nil
			}

			continue
		}

		if current != "" {
			stages[current] = append(stages[current], strings.TrimRight(line, "\n"))
		}
	}

	if len(stages) == 0 {
		t.Fatal("the Dockerfile has no named stages")
	}

	return stages
}
