#!/usr/bin/env bats
#
# Acceptance checks for the published runtime images, run by both ci.yml and
# `task image:smoke`.
#
# IMAGE and EXPECTED_VERSION name the image under test and the version it must
# report. TARGET is the Dockerfile stage it was built from: the two stages ship
# the same binary on different bases, so a handful of checks belong to one of
# them and are skipped on the other.

setup_file() {
    bats_require_minimum_version 1.5.0

    : "${IMAGE:?set IMAGE to the image reference under test}"
    : "${EXPECTED_VERSION:?set EXPECTED_VERSION to the version the image must report}"
    : "${TARGET:?set TARGET to the Dockerfile stage the image was built from}"
}

setup() {
    bats_load_library bats-support
    bats_load_library bats-assert

    # Under TMPDIR, which points at a directory mounted at the same path on the
    # host — the daemon resolves the bind mounts below against the host.
    WORKDIR="$(mktemp -d)"

    # The images run as 65534, which is nobody and is not whoever created
    # WORKDIR. A 0700 directory would fail the bind-mount checks on traversal
    # permissions rather than on anything they are about.
    chmod 0755 "$WORKDIR"
}

teardown() {
    rm -rf "$WORKDIR"
}

# Runs the image against a config bind-mounted read-only, the way the CI recipe
# tells people to mount theirs.
run_with_config() {
    local body="$1"
    shift

    printf '%s\n' "$body" > "$WORKDIR/labels.yml"
    chmod 0644 "$WORKDIR/labels.yml"

    docker run --rm \
        --volume "$WORKDIR/labels.yml:/labels.yml:ro" \
        "$IMAGE" "$@"
}

# Copies a path out of the image without running it — the scratch image has no
# shell to read a file with. The container is removed whether or not the copy
# worked, and the copy's status is what the caller sees.
extract() {
    local path="$1" dest="$2" cid status=0

    cid="$(docker create "$IMAGE")"
    docker cp "$cid:$path" "$dest" || status=$?
    docker rm --force "$cid" > /dev/null

    return "$status"
}

@test "reports the version injected at build time" {
    run docker run --rm "$IMAGE" version --dont-prettify

    assert_success
    # "dev" here means LABELSYNC_VERSION never reached the ldflag — which is
    # what a renamed build arg looks like: buildx warns, the build succeeds.
    assert_output "$EXPECTED_VERSION"
}

@test "runs as nobody rather than root" {
    run docker inspect --format '{{.Config.User}}' "$IMAGE"

    assert_success
    assert_output "65534:65534"
}

@test "ships the CA bundle where Go looks for it" {
    run extract /etc/ssl/certs/ca-certificates.crt "$WORKDIR/ca-certificates.crt"

    # Without the trailing slash on the COPY the bundle lands as a file named
    # /etc/ssl/certs, this copy fails, and every API call would have failed
    # with "failed to load system roots" long after the version check passed.
    assert_success
    assert [ -s "$WORKDIR/ca-certificates.crt" ]
}

@test "parses a bind-mounted config as the unprivileged user" {
    run run_with_config 'version: 1

labels:
  - name: "type: bug"
    color: "d73a4a"' sync --config /labels.yml --output=json

    # Nothing in the container can produce a token, so the run stops at
    # resolving one — which is the assertion: reaching the token means the
    # mounted file was readable and the YAML parsed.
    assert_failure 1
    assert_output --partial '"error_kind":"no_token"'
}

@test "reports a config error from the mounted file" {
    run run_with_config 'version: 1

labels:
  - name: "type: bug"
    color: "nope"' sync --config /labels.yml --output=json

    assert_failure 1
    assert_output --partial '"error_kind":"invalid_color"'
}

@test "the debian image has a shell with labelsync on PATH" {
    [ "$TARGET" = debian ] || skip "the $TARGET stage is scratch and ships no shell"

    run docker run --rm --entrypoint bash "$IMAGE" -c 'command -v labelsync'

    assert_success
    assert_output "/usr/local/bin/labelsync"
}

@test "the scratch image gives its uid a name" {
    [ "$TARGET" = binary ] || skip "the $TARGET stage inherits a passwd file from its base"

    run extract /etc/passwd "$WORKDIR/passwd"
    assert_success

    run grep -q '^nobody:' "$WORKDIR/passwd"
    assert_success
}
