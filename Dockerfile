# Build stage: static binary, no cgo, pinned toolchain from go.mod.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/mage2mcp-server ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/mage2mcp-worker ./cmd/worker \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/mage2mcp-admin ./cmd/admin

# Runtime stage: distroless static, non-root, no shell.
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
LABEL org.opencontainers.image.title="mage2mcp" \
      org.opencontainers.image.description="Magento 2 MCP gateway: typed, audited tools for Magento, Cloudflare, and GA4" \
      org.opencontainers.image.source="https://github.com/shuaiZend/mage2mcp" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=build /out/mage2mcp-server /usr/local/bin/mage2mcp-server
COPY --from=build /out/mage2mcp-worker /usr/local/bin/mage2mcp-worker
COPY --from=build /out/mage2mcp-admin /usr/local/bin/mage2mcp-admin
COPY configs/config.docker.yaml /etc/mage2mcp/config.yaml

USER nonroot:nonroot
EXPOSE 8080
# Distroless has no shell or curl; the binary probes its own /healthz.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/usr/local/bin/mage2mcp-server", "-healthcheck"]
ENTRYPOINT ["/usr/local/bin/mage2mcp-server"]
CMD ["-config", "/etc/mage2mcp/config.yaml"]
