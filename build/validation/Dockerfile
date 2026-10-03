ARG GO_IMAGE=golang:1.26.3-bookworm
FROM node:22-bookworm-slim AS node
FROM ${GO_IMAGE}
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
ARG DEBIAN_MIRROR=mirrors.ustc.edu.cn
# Only this disposable image uses the mirror; Debian's archive signatures
# remain enforced. See https://mirrors.ustc.edu.cn/help/debian.html.
RUN --mount=type=cache,target=/var/cache/apt --mount=type=cache,target=/var/lib/apt/lists \
    sed -i "s|http://deb.debian.org|https://${DEBIAN_MIRROR}|g" /etc/apt/sources.list.d/debian.sources \
    && ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && apt-get update && apt-get install -y --no-install-recommends \
       make gcc g++ pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev fonts-noto-cjk \
    && npm install --prefix /opt/validation --no-audit --no-fund playwright@1.62.1 \
    && /opt/validation/node_modules/.bin/playwright install --with-deps chromium webkit \
    && rm -rf /var/lib/apt/lists/*
ENV NODE_PATH=/opt/validation/node_modules
ENV GOMAXPROCS=4
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/root/.cache/go-build \
    make release-windows VERSION="${VERSION}" \
    && mkdir -p /out \
    && cp dist/magpie-windows-amd64.exe /out/magpie-windows-amd64.exe \
    && CGO_ENABLED=0 go build -tags nogui -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/magpie .
ENV GOFLAGS=-tags=gtk3
ENTRYPOINT ["bash", "/src/build/validation/run.sh"]
CMD ["verify"]
