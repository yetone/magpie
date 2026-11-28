FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download

COPY . .

ARG VERSION=dev
# grpcnotrace: as LEAN in the Makefile.
RUN CGO_ENABLED=0 go build -tags nogui,grpcnotrace -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/magpie . \
    && mkdir -p /config/home /config/cache /config/data /config/state

# The runtimes the Library's MCP servers start with: npx (most of them,
# chrome-devtools among them: "npx not found", Passings_z on X), node and
# npm (magpie installs an npx server's package and runs it with node,
# mcp_npx.go), and uvx. Node is the official build, taken from the image
# of the same Debian release as the one below; uv's static binaries
# download the Python a uvx server needs into the volume the first time.
FROM node:24.22.3-trixie-slim AS node
FROM ghcr.io/astral-sh/uv:0.13.7 AS uv

# Debian, not distroless: a subscription's CLI is installed with its
# vendor's `curl … | bash` (Cursor, Devin, Grok Build), and the CLI it
# installs is a glibc binary that needs a full system's loader and
# libraries. distroless with a copied curl ran the installer, but Cursor's
# cursor-agent then failed with "cannot execute: required file not found".
# Debian has the glibc, libgcc and libstdc++ the plugins' Bun needs too,
# and bash for a NAS panel's terminal. Debian 13, not 12: an npm package's
# own binary built on a newer system needs glibc 2.38 or later, and Debian
# 12's 2.36 crashed an MCP server's Rust binary at once (Passings_z on X,
# "GLIBC_2.38 not found"); 13 has 2.41. nonroot keeps distroless's uid 65532,
# so a volume made by an older image stays writable.
FROM debian:13-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 65532 nonroot \
    && useradd -u 65532 -g nonroot -d /config/home -s /bin/bash nonroot

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
COPY --from=uv /uv /uvx /usr/local/bin/
# python and python3 are uv's own Python, downloaded once, so a server
# started as `python3 server.py` runs too. uv takes only its own Pythons
# (UV_PYTHON_PREFERENCE below): one found on PATH would be this script,
# which asks uv again, and uvx hung looking for an interpreter.
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && printf '#!/bin/sh\nexec uv run --no-project --quiet python "$@"\n' > /usr/local/bin/python3 \
    && chmod 755 /usr/local/bin/python3 \
    && ln -s python3 /usr/local/bin/python

COPY --from=build /out/magpie /magpie
# magpie on PATH, so `magpie accounts add …` works in that terminal
RUN ln -s /magpie /usr/local/bin/magpie
USER nonroot
COPY --from=build --chown=65532:65532 /config /config

# Everything magpie and the sign-ins it manages write goes into the volume:
# its own files (/config/magpie), sign-ins kept where an agent keeps them
# (~/.codex, ~/.claude…, under HOME), and the cache holding Bun and the
# model catalog. magpie makes the folders a volume from an older image
# lacks.
ENV HOME=/config/home \
    XDG_CONFIG_HOME=/config \
    XDG_CACHE_HOME=/config/cache \
    XDG_DATA_HOME=/config/data \
    XDG_STATE_HOME=/config/state \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    UV_PYTHON_PREFERENCE=only-managed \
    UV_PYTHON=3.14 \
    MAGPIE_ADDR=0.0.0.0:3425

VOLUME /config

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/magpie", "healthcheck"]

EXPOSE 3425 3430

ENTRYPOINT ["/magpie"]
CMD ["serve"]
