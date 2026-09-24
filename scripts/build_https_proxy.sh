#!/bin/sh
# Cross-build the standalone HTTPS reverse proxy (cmd/https-proxy).
# Output: dist/https-proxy.exe (Windows amd64) and dist/https-proxy (host OS)
set -e
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -trimpath -o dist/https-proxy.exe ./cmd/https-proxy
CGO_ENABLED=0 go build -ldflags "-s -w" -trimpath -o dist/https-proxy ./cmd/https-proxy
echo "Built dist/https-proxy.exe and dist/https-proxy"
