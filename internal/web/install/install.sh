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
# Download beside the target, then rename over it.
#
# Writing straight to $dir/csx truncates the file in place, and after
# `csx init` that file is a RUNNING process — the MCP server the editor
# started. Truncating it under a running program corrupts it mid-execution
# and, if the download then fails, leaves nothing installed at all. rename
# is atomic: the running server keeps the file it already opened and the
# next start gets the new one.
staged="$dir/.csx.download.$$"
trap 'rm -f "$staged"' EXIT
if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$base/dl/csx-$os-$arch" -o "$staged"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$staged" "$base/dl/csx-$os-$arch"
else
    echo "csx: need curl or wget" >&2
    exit 1
fi
chmod +x "$staged"
upgrade=0
[ -e "$dir/csx" ] && upgrade=1
mv -f "$staged" "$dir/csx"

case ":$PATH:" in
    *":$dir:"*) ;;
    *)
        echo "NOTE: $dir is not on your PATH. Add it with:"
        echo "  export PATH=\"$dir:\$PATH\""
        ;;
esac

echo "csx installed. Starting setup..."
if [ "$upgrade" = "1" ]; then
    echo
    echo "You upgraded an existing install. If your editor is open, restart it:"
    echo "the csx MCP server it started is still running the previous build."
fi
if [ "${CSX_WORKER_ONLY:-}" = "1" ]; then
    exec "$dir/csx" init --community --yes --no-agents --no-daemon
fi
exec "$dir/csx" init
