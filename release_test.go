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
	Permissions map[string]string     `yaml:"permissions"`
	Jobs        map[string]releaseJob `yaml:"jobs"`
}

// A job that calls a reusable workflow has `uses`/`with` where a normal one has
// `steps`, so both shapes live on one struct and each test reads the half it
// cares about.
type releaseJob struct {
	Needs       string            `yaml:"needs"`
	Uses        string            `yaml:"uses"`
	With        map[string]any    `yaml:"with"`
	Permissions map[string]string `yaml:"permissions"`
	Strategy    struct {
		Matrix struct {
			Runner []matrixRunner `yaml:"runner"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
}

// matrixRunner is one leg of an image build: the runner it runs on and the
// platform it builds for. Shared with the pull-request guard in ci_test.go,
// which pairs them the same way.
type matrixRunner struct {
	OS       string `yaml:"os"`
	Platform string `yaml:"platform"`
}

// The image name every stage publishes under. The debian stage is a tag suffix
// on this name rather than a package of its own — see the architecture docs.
const imageName = "ghcr.io/specsnl/labelsync"

// The Dockerfile ARG the version is injected through, which the shared workflow
// has to be told the name of.
const versionBuildArg = "LABELSYNC_VERSION"

// buildJobs maps each build job to the manifest job that merges its digests,
// and publishedTargets maps each build job to the Dockerfile stage it ships.
var (
	buildJobs = map[string]string{
		"image":        "image-manifest",
		"image-debian": "image-debian-manifest",
	}
	publishedTargets = map[string]string{
		"image":        "binary",
		"image-debian": "debian",
	}
)

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

	for build, manifest := range buildJobs {
		for _, name := range []string{build, manifest} {
			if _, ok := workflow.Jobs[name]; !ok {
				t.Fatalf("the release workflow has no %q job; jobs are %v",
					name, slices.Sorted(maps.Keys(workflow.Jobs)))
			}
		}
	}

	return workflow
}

// with reads one `with:` input as a string. The block mixes strings and
// booleans, so everything comes back through fmt.Sprint rather than typed per
// key.
func (j releaseJob) with(t *testing.T, key string) string {
	t.Helper()

	value, ok := j.With[key]
	if !ok {
		t.Fatalf("the job has no %q input; it has %v", key, slices.Sorted(maps.Keys(j.With)))
	}

	return fmt.Sprint(value)
}

// Two images, from two stages of one Dockerfile, both published under one name:
// the scratch stage unsuffixed and debian as a `-debian` tag suffix, which is
// how the org publishes a CLI's base-image variants. A build job without its
// manifest job pushes digests nothing ever merges into a tag — a release that
// goes green and publishes nothing pullable.
func TestRelease_ImageJobsPublishBothStages(t *testing.T) {
	workflow := loadReleaseWorkflow(t)
	stages := dockerfileStages(t)

	for build, manifest := range buildJobs {
		target := publishedTargets[build]

		if _, ok := stages[target]; !ok {
			t.Errorf("the Dockerfile has no %q stage for the %s job; stages are %v",
				target, build, slices.Sorted(maps.Keys(stages)))
		}

		for _, name := range []string{build, manifest} {
			job := workflow.Jobs[name]

			if got := job.with(t, "image-name"); got != imageName {
				t.Errorf("%s publishes %q, want %q", name, got, imageName)
			}

			if got := job.with(t, "target"); got != target {
				t.Errorf("%s builds target %q, want %q", name, got, target)
			}
		}

		if got := workflow.Jobs[manifest].Needs; got != build {
			t.Errorf("%s needs %q, want %q — a manifest merges digests its build job has to have pushed",
				manifest, got, build)
		}
	}

	// The variant is what turns debian into a tag suffix. Without it the two
	// manifests write the same tags from different digests, and whichever
	// finishes last owns `:1.2.3`.
	if got := workflow.Jobs["image-debian-manifest"].with(t, "variant"); got != "debian" {
		t.Errorf("image-debian-manifest variant = %q, want %q", got, "debian")
	}

	if _, ok := workflow.Jobs["image-manifest"].With["variant"]; ok {
		t.Error("image-manifest sets a variant, which would suffix the tags the scratch image publishes as the default")
	}
}

// One runner per architecture, each building for its own platform. Pointing both
// legs at the same runner still produces a manifest list — buildx would emulate
// the foreign one — and the release succeeds either way, so nothing but this
// says which happened.
func TestRelease_ImageJobsBuildOnANativeRunnerPerPlatform(t *testing.T) {
	workflow := loadReleaseWorkflow(t)

	want := map[string]string{
		"linux/amd64": "ubuntu-24.04",
		"linux/arm64": "ubuntu-24.04-arm",
	}

	for build := range buildJobs {
		runners := workflow.Jobs[build].Strategy.Matrix.Runner

		if len(runners) != len(want) {
			t.Errorf("%s has %d matrix legs, want %d", build, len(runners), len(want))
		}

		seen := map[string]bool{}

		for _, runner := range runners {
			native, ok := want[runner.Platform]
			if !ok {
				t.Errorf("%s builds unexpected platform %q", build, runner.Platform)

				continue
			}

			if runner.OS != native {
				t.Errorf("%s builds %s on %q, want %q", build, runner.Platform, runner.OS, native)
			}

			seen[runner.Platform] = true
		}

		for platform := range want {
			if !seen[platform] {
				t.Errorf("%s does not build %s; an arm64 host pulling the release would fail at `docker run`",
					build, platform)
			}
		}
	}
}

// The shared workflow injects the version through the build arg it is named
// here, and a wrong name fails silently: buildx warns about an unused arg, the
// build succeeds, and the published image reports `dev`.
func TestRelease_ImageJobsNameTheVersionBuildArg(t *testing.T) {
	workflow := loadReleaseWorkflow(t)

	for build := range buildJobs {
		if got := workflow.Jobs[build].with(t, "version-build-arg"); got != versionBuildArg {
			t.Errorf("%s injects the version through %q, want %q", build, got, versionBuildArg)
		}
	}

	if !strings.Contains(readDockerfile(t), "ARG "+versionBuildArg+"=") {
		t.Errorf("the Dockerfile declares no ARG %s for the workflow to pass the version to", versionBuildArg)
	}
}

// Pushing to GHCR needs `packages: write`, which the workflow-level block does
// not grant: it is read-only so each job asks for its own write scope. A
// reusable workflow inherits nothing it is not given, so the grant has to sit on
// the calling job — without it the release goes green and the push fails.
func TestRelease_ImageJobsCanWritePackages(t *testing.T) {
	workflow := loadReleaseWorkflow(t)

	for build, manifest := range buildJobs {
		for _, name := range []string{build, manifest} {
			if got := workflow.Jobs[name].Permissions["packages"]; got != "write" {
				t.Errorf("the %s job has packages: %q, want %q", name, got, "write")
			}
		}
	}

	if got := workflow.Jobs["release"].Permissions["contents"]; got != "write" {
		t.Errorf("the release job has contents: %q, want %q", got, "write")
	}
}

// The tag policy, the login, the digest merge and the OCI labels all live in
// specsnl/github-actions now, so what this repository still owns is the pin. A
// half-finished bump — one job moved, three left behind — builds digests with
// one version of the pipeline and merges them with another.
func TestRelease_SharedWorkflowsArePinnedToOneRef(t *testing.T) {
	refs := map[string][]string{}

	for _, path := range []string{".github/workflows/release.yml", ".github/workflows/ci.yml"} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		for line := range strings.Lines(string(content)) {
			_, after, ok := strings.Cut(line, "specsnl/github-actions")
			if !ok {
				continue
			}

			_, ref, ok := strings.Cut(after, "@")
			if !ok {
				t.Errorf("%s uses specsnl/github-actions without a ref:\n%s", path, strings.TrimSpace(line))

				continue
			}

			ref = strings.TrimSpace(ref)
			refs[ref] = append(refs[ref], path+": "+strings.TrimSpace(line))
		}
	}

	if len(refs) == 0 {
		t.Fatal("neither workflow calls specsnl/github-actions; the image pipeline is not shared at all")
	}

	if len(refs) > 1 {
		t.Errorf("the shared workflows are pinned to %d different refs: %v",
			len(refs), slices.Sorted(maps.Keys(refs)))
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

// The release builds each platform on its own runner, so nothing there is ever
// emulated — but a `--platform` build anywhere else is, unless the builder stays
// pinned to the platform doing the building and takes its GOOS/GOARCH from the
// platform being built for. Unpin either and `docker buildx build --platform
// linux/arm64` on a laptop runs apt-get and the whole compile under QEMU.
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
