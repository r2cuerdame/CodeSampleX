#!/bin/sh
# CodeSampleX installer for macOS / Linux.
# Usage:  curl -fsSL https://codesamplex.dev/install.sh | sh
# Downloads the single csx binary into ~/.local/bin, then runs
# `csx init` (one question, everything else automatic).

set -eu

base="__CSX_BASE_URL__"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
    linux|darwin) ;;
    *) echo "csx: unsupported OS: $os" >&2; exit 1 ;;
esac

arch=$(uname -m)
case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) echo "csx: unsupported architecture: $arch" >&2; exit 1 ;;
esac

dir="${CSX_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir"

echo "Downloading csx ($os/$arch) from $base ..."
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$base/dl/csx-$os-$arch" -o "$dir/csx"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dir/csx" "$base/dl/csx-$os-$arch"
else
    echo "csx: need curl or wget" >&2
    exit 1
fi
chmod +x "$dir/csx"

case ":$PATH:" in
    *":$dir:"*) ;;
    *)
        echo "NOTE: $dir is not on your PATH. Add it with:"
        echo "  export PATH=\"$dir:\$PATH\""
        ;;
esac

echo "csx installed. Starting setup..."
exec "$dir/csx" init
