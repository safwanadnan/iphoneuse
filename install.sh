#!/bin/sh
# iPhoneUse installer: downloads the latest release for this Mac and puts
# `iphoneuse` on your PATH. Usage:
#   curl -fsSL https://raw.githubusercontent.com/safwanadnan/iphoneuse/main/install.sh | sh
# Set IPHONEUSE_INSTALL_DIR to choose where the binary goes.
set -eu

REPO="safwanadnan/iphoneuse"

say() { printf '%s\n' "$*"; }
fail() { printf 'iPhoneUse install: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Darwin" ] || fail "iPhoneUse needs a Mac (it uses Xcode to talk to your iPhone)."

case "$(uname -m)" in
  arm64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) fail "unsupported CPU: $(uname -m)" ;;
esac

TAG=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
[ -n "$TAG" ] || fail "could not find the latest release"
VERSION=${TAG#v}
FILE="iphoneuse_${VERSION}_darwin_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$TAG"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
say "Downloading iPhoneUse $VERSION…"
curl -fsSL "$BASE/$FILE" -o "$TMP/$FILE"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt"
( cd "$TMP" && grep " $FILE\$" checksums.txt | shasum -a 256 -c - >/dev/null ) || fail "checksum mismatch; not installing"
tar -xzf "$TMP/$FILE" -C "$TMP"

if [ -n "${IPHONEUSE_INSTALL_DIR:-}" ]; then
  DEST="$IPHONEUSE_INSTALL_DIR"
elif [ -w /usr/local/bin ] || { [ ! -e /usr/local/bin ] && [ -w /usr/local ]; }; then
  DEST=/usr/local/bin
else
  DEST="$HOME/.local/bin"
fi
mkdir -p "$DEST"
mv "$TMP/iphoneuse" "$DEST/iphoneuse"
chmod +x "$DEST/iphoneuse"
xattr -d com.apple.quarantine "$DEST/iphoneuse" 2>/dev/null || true

say "Installed $DEST/iphoneuse"
case ":$PATH:" in
  *":$DEST:"*) ;;
  *) say "Add it to your PATH:  echo 'export PATH=\"$DEST:\$PATH\"' >> ~/.zshrc && source ~/.zshrc" ;;
esac
say ""
say "Next: plug in your iPhone, unlock it, and run:"
say "  iphoneuse setup"
