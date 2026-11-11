# syntax=docker/dockerfile:1
#
# The image that runs the updates (docs/operations.md): jwd, the hf CLI and
# the scripts. Released as ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z
# by .github/workflows/release.yml (docs/release.md). Base images are pinned
# by digest, the hf CLI by version and date; refresh them by hand.

FROM golang:1.26.11-trixie@sha256:fa340cb59ebeb1d53ac1cccfc334201d938fe5f3458003cccb767096d8e032b8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
# cgo for DuckDB.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/jwd ./cmd/jwd

FROM ghcr.io/astral-sh/uv:0.13.7@sha256:96f40d3904aad8d0bd010539001d36c8e2188b981e1334471a90c4a1d93bb490 AS uv

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS runtime
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates jq tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --uid 1000 --user-group --no-create-home --home-dir /tmp jwd
COPY --from=uv /uv /usr/local/bin/uv
# hf CLI with its dependencies as of a fixed date; it gets HF_TOKEN.
ENV UV_TOOL_DIR=/opt/uv/tools UV_TOOL_BIN_DIR=/usr/local/bin UV_PYTHON_INSTALL_DIR=/opt/uv/python
RUN uv tool install --exclude-newer 2026-11-12T00:00:00Z huggingface_hub==2.4.0 \
 && rm -rf /root/.cache \
 && hf version
COPY --from=build /out/jwd /opt/jwd/bin/jwd
COPY scripts/ /opt/jwd/scripts/

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="japan-weather-dataset" \
      org.opencontainers.image.description="jwd: builds and updates a Parquet dataset of JMA weather observations" \
      org.opencontainers.image.source="https://github.com/hotchpotch/japan-weather-dataset" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.licenses="MIT"

# Nothing is written outside the mounted directories (/data, /dataset) and
# /tmp, so the image also runs as the calling user (docker run --user).
ENV PATH=/opt/jwd/bin:/opt/jwd/scripts:$PATH HOME=/tmp HF_HOME=/tmp/hf \
    JWD_DATA_DIR=/data JWD_EXPORT_DIR=/dataset HF_HUB_DISABLE_TELEMETRY=1
USER jwd
WORKDIR /tmp
CMD ["jwd", "-h"]
