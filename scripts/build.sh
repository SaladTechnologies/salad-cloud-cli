#!/usr/bin/env bash
# Cross-compiles the SaladCloud CLI for linux/amd64, darwin/arm64, darwin/amd64 and windows/amd64.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo none)
MODULE=github.com/saladtechnologies/salad-cloud-cli
LDFLAGS="-s -w -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT}"

export CGO_ENABLED=0

GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" -o dist/windows-amd64/saladcloud.exe ./cmd/saladcloud
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" -o dist/linux-amd64/saladcloud ./cmd/saladcloud
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "${LDFLAGS}" -o dist/darwin-arm64/saladcloud ./cmd/saladcloud
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" -o dist/darwin-amd64/saladcloud ./cmd/saladcloud

echo "built windows-amd64, linux-amd64, darwin-arm64, darwin-amd64 (version ${VERSION}, commit ${COMMIT})"
