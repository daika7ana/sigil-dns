#!/usr/bin/env bash
# Build the Windows release binaries into dist/ (CLI + GUI).
#
#   ./dev/build.sh              # windows/amd64, version from git describe
#   VERSION=v1.2.3 ./dev/build.sh
set -eu

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

export GOOS="${GOOS:-windows}"
export GOARCH="${GOARCH:-amd64}"

version="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
stamp="-X github.com/daika7ana/dns-client/internal/version.Version=$version"

mkdir -p dist

echo "Building dns-client.exe ($version)"
./dev/go build -trimpath -ldflags "-s -w $stamp" -o dist/dns-client.exe ./cmd/dns-client

echo "Building dns-client-gui.exe ($version)"
./dev/go build -trimpath -tags gui -ldflags "-s -w -H windowsgui $stamp" -o dist/dns-client-gui.exe ./cmd/dns-client

ls -l dist/*.exe
