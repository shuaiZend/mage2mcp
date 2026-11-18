# syntax=docker/dockerfile:1

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
      -o /out/mage2mcp ./cmd/server

# Runtime stage: distroless static, non-root, no shell.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mage2mcp /usr/local/bin/mage2mcp
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mage2mcp"]
CMD ["-config", "/etc/mage2mcp/config.yaml"]
