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
stamp="-X github.com/daika7ana/sigil-dns/internal/version.Version=$version"

mkdir -p dist

echo "Building sigildns.exe ($version)"
./dev/go build -trimpath -ldflags "-s -w $stamp" -o dist/sigildns.exe ./cmd/sigildns

echo "Building sigildns-gui.exe ($version)"
./dev/go build -trimpath -tags gui -ldflags "-s -w -H windowsgui $stamp" -o dist/sigildns-gui.exe ./cmd/sigildns

ls -l dist/*.exe
