# syntax=docker/dockerfile:1
# check=error=true

# Latest version: https://hub.docker.com/_/golang/tags
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie AS base

WORKDIR /src

RUN apt-get update \
    && apt-get install --assume-yes --no-install-recommends \
        ca-certificates \
        tree \
        git \
        openssh-client \
    && rm -rf /var/lib/apt/lists/*

FROM base AS builder-download

COPY go.mod .
COPY go.sum .

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

FROM builder-download AS build

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG GOOS
ARG GOARCH
ARG GO_MODULE=github.com/specsnl/labelsync
ARG LABELSYNC_VERSION=dev

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go generate \
    && CGO_ENABLED=0 GOOS=${GOOS:-$TARGETOS} GOARCH=${GOARCH:-$TARGETARCH} go build \
        -trimpath \
        -tags netgo \
        -ldflags "-s -w -X ${GO_MODULE}/internal/cmd.Version=${LABELSYNC_VERSION}" -o ./labelsync

# Latest version: https://hub.docker.com/r/bats/bats/tags
FROM bats/bats:1.14.0 AS bats

ARG TARGETARCH

# Latest version: https://download.docker.com/linux/static/stable/
ARG DOCKER_VERSION=29.8.0
# Latest version: https://github.com/bats-core/bats-support/releases/latest
ARG BATS_SUPPORT_VERSION=0.3.0
# Latest version: https://github.com/bats-core/bats-assert/releases/latest
ARG BATS_ASSERT_VERSION=2.2.4

# busybox ash, since this stage is Alpine and carries no bash.
SHELL ["/bin/ash", "-o", "pipefail", "-c"]

RUN apk add --no-cache \
    curl \
    tar

RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) altarch=x86_64 ;; \
        arm64) altarch=aarch64 ;; \
        *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl --fail --silent --show-error --location \
        "https://download.docker.com/linux/static/stable/${altarch}/docker-${DOCKER_VERSION}.tgz" \
        | tar --extract --gzip --directory /usr/bin --strip-components=1 docker/docker; \
    for spec in "support:${BATS_SUPPORT_VERSION}" "assert:${BATS_ASSERT_VERSION}"; do \
        name="bats-${spec%%:*}"; \
        mkdir -p "/usr/lib/bats/${name}"; \
        curl --fail --silent --show-error --location \
            "https://github.com/bats-core/${name}/archive/refs/tags/v${spec#*:}.tar.gz" \
            | tar --extract --gzip --directory "/usr/lib/bats/${name}" --strip-components=1; \
    done

ENV BATS_LIB_PATH=/usr/lib/bats

# Latest version: https://hub.docker.com/_/debian/tags
FROM debian:13.6-slim AS debian

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /src/labelsync /usr/local/bin

USER 65534:65534

ENTRYPOINT ["labelsync"]

FROM scratch AS binary

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /src/labelsync /
COPY --from=build /etc/passwd /etc/passwd

USER 65534:65534

ENTRYPOINT ["/labelsync"]

FROM scratch AS export

COPY --from=build /src/labelsync /labelsync
