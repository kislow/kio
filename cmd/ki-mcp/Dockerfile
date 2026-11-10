# Build from the repo root: docker build -f cmd/ki-mcp/Dockerfile .
FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
# Declared here, not at the top: every RUN after an ARG misses the layer cache
# when its value changes, and it changes with every release.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/ki-mcp ./cmd/ki-mcp

FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/kislow/kio" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/ki-mcp /ki-mcp
COPY LICENSE NOTICE /usr/share/doc/kio/
USER nonroot:nonroot
EXPOSE 8787
ENTRYPOINT ["/ki-mcp", "-config", "/etc/ki/config.json"]
