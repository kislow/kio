# Build from the repo root: docker build -f vault/Dockerfile .
# The vault pusher: obsidian-headless keeps a folder in step with Obsidian Sync, ki vault push sends it to git.
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
# ki embeds the agent instructions and skills through the root package.
COPY embed.go ./
COPY agent ./agent
COPY skills ./skills
# Declared here, not at the top: every RUN after an ARG misses the layer cache
# when its value changes, and it changes with every release.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ki ./cmd/ki

# Node 24 is the newest LTS line; obsidian-headless needs Node 22 or later.
FROM node:24.22.1-bookworm-slim
ARG OBSIDIAN_HEADLESS_VERSION=0.0.14
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git openssh-client tini \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --no-fund --no-audit "obsidian-headless@${OBSIDIAN_HEADLESS_VERSION}" \
 && npm cache clean --force
# After the install, so a new version does not rerun it (see the build stage).
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/kislow/kio" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/ki /usr/local/bin/ki
COPY LICENSE NOTICE /usr/share/doc/kio/
COPY vault/sync.sh /usr/local/bin/vault-sync
COPY vault/pusher.sh /usr/local/bin/vault-pusher
# The node image's own non-root user, uid 1000.
USER node
ENTRYPOINT ["/usr/bin/tini", "--"]
