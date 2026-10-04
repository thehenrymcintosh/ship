#!/bin/sh
# Install ship from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/thehenrymcintosh/ship/main/install.sh | sh
#
# Options (environment variables):
#   SHIP_VERSION      release tag to install, e.g. v0.2.0 (default: latest)
#   SHIP_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#
# Afterwards, `ship update` keeps it up to date.
set -eu

REPO="thehenrymcintosh/ship"
GITHUB="${SHIP_GITHUB_URL:-https://github.com}" # overridable for testing
INSTALL_DIR="${SHIP_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${SHIP_VERSION:-}"

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS: $(uname -s) (macOS and Linux only)" ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
  latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$GITHUB/$REPO/releases/latest") ||
    die "couldn't reach github.com"
  case "$latest" in
    */tag/*) VERSION=${latest##*/tag/} ;;
    *) die "no release of $REPO has been published yet" ;;
  esac
fi
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac

asset="ship_${VERSION#v}_${os}_${arch}.tar.gz"
base="$GITHUB/$REPO/releases/download/$VERSION"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading ship $VERSION ($os/$arch)…"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "no build for $os/$arch in $VERSION"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "couldn't download checksums.txt"

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$asset isn't listed in checksums.txt"
if command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
else
  got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
fi
[ "$got" = "$want" ] || die "checksum mismatch for $asset"

tar -xzf "$tmp/$asset" -C "$tmp" ship
mkdir -p "$INSTALL_DIR"
chmod 0755 "$tmp/ship"
mv -f "$tmp/ship" "$INSTALL_DIR/ship"

say "Installed $("$INSTALL_DIR/ship" version | head -n 1) to $INSTALL_DIR/ship"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say ""
     say "$INSTALL_DIR isn't on your PATH. Add this to your shell profile:"
     say "  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
daemon=$("$INSTALL_DIR/ship" version 2>/dev/null | awk '/^daemon / { print $2 }')
if [ -n "$daemon" ] && [ "$daemon" != "${VERSION#v}" ]; then
  say "A ship daemon is still running $daemon; switch it over with: ship serve --restart"
fi
say ""
say "Get started: cd your-repo && ship init"
