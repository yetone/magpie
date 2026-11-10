FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -tags nogui -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" -o /out/magpie . \
    && mkdir -p /config/home /config/cache /config/data /config/state

# Debian, not distroless: a subscription's CLI is installed with its
# vendor's `curl … | bash` (Cursor, Devin, Grok Build), and the CLI it
# installs is a glibc binary that needs a full system's loader and
# libraries. distroless with a copied curl ran the installer, but Cursor's
# cursor-agent then failed with "cannot execute: required file not found".
# Debian 12 has the glibc, libgcc and libstdc++ the plugins' Bun needs too,
# and bash for a NAS panel's terminal. nonroot keeps distroless's uid 65532,
# so a volume made by an older image stays writable.
FROM debian:12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 65532 nonroot \
    && useradd -u 65532 -g nonroot -d /config/home -s /bin/bash nonroot

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
    MAGPIE_ADDR=0.0.0.0:3425

VOLUME /config

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/magpie", "healthcheck"]

EXPOSE 3425 3430

ENTRYPOINT ["/magpie"]
CMD ["serve"]
