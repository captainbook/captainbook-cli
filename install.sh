#!/bin/sh
set -e

REPO="captainbook/captainbook-cli"
BINARY="ceebee"

# Install directory resolution.
#
# 1. Honour PREFIX if the caller set it explicitly (e.g.
#    `curl … | PREFIX=$HOME/.local/bin sh`). The leading expansion of
#    `~` is the shell's job — we don't try to expand it ourselves.
# 2. Otherwise prefer $HOME/.local/bin when it exists or is creatable
#    *and* the caller does not need sudo. This is the default that
#    matches modern Linux/macOS user layouts and lets operators avoid
#    sudo entirely.
# 3. Fall back to /usr/local/bin (the historic default) and gate the
#    move on `sudo` if needed.
if [ -n "$PREFIX" ]; then
  INSTALL_DIR="$PREFIX"
else
  USER_BIN="${HOME}/.local/bin"
  if [ -d "$USER_BIN" ] || mkdir -p "$USER_BIN" 2>/dev/null; then
    INSTALL_DIR="$USER_BIN"
  else
    INSTALL_DIR="/usr/local/bin"
  fi
fi

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Linux*)  GOOS="linux" ;;
  Darwin*) GOOS="darwin" ;;
  *)       echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  GOARCH="amd64" ;;
  arm64|aarch64)  GOARCH="arm64" ;;
  *)              echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

ASSET="${BINARY}-${GOOS}-${GOARCH}"

echo "Detected platform: ${GOOS}/${GOARCH}"
echo "Install target: ${INSTALL_DIR}"

# Get latest release tag
TAG="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
if [ -z "$TAG" ]; then
  echo "Failed to determine latest release" >&2
  exit 1
fi
echo "Latest release: ${TAG}"

# Download binary
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
echo "Downloading ${URL}..."
TMPFILE="$(mktemp)"
trap 'rm -f "$TMPFILE"' EXIT
curl -fsSL -o "$TMPFILE" "$URL"

# Verify checksum
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/checksums.txt"
EXPECTED="$(curl -fsSL "$CHECKSUMS_URL" | grep "  ${ASSET}$" | awk '{print $1}')" || true
if [ -z "$EXPECTED" ]; then
  echo "Error: could not retrieve checksum for ${ASSET}" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "$TMPFILE" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL="$(shasum -a 256 "$TMPFILE" | awk '{print $1}')"
else
  echo "Error: no sha256 tool found (need sha256sum or shasum)" >&2
  exit 1
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "Checksum mismatch!" >&2
  echo "  Expected: ${EXPECTED}" >&2
  echo "  Got:      ${ACTUAL}" >&2
  exit 1
fi
echo "Checksum verified."

# Ensure install dir exists. For user-owned paths we create it ourselves;
# for system paths the caller is expected to have it already (sudo will
# handle the move).
if [ ! -d "$INSTALL_DIR" ]; then
  if mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    :
  else
    echo "Creating ${INSTALL_DIR} (requires sudo)..."
    sudo mkdir -p "$INSTALL_DIR"
  fi
fi

# Install
chmod 755 "$TMPFILE"
if [ -w "$INSTALL_DIR" ]; then
  mv "$TMPFILE" "${INSTALL_DIR}/${BINARY}"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "$TMPFILE" "${INSTALL_DIR}/${BINARY}"
fi

echo "Installed ${BINARY} ${TAG} to ${INSTALL_DIR}/${BINARY}"

# PATH check — warn if the install dir isn't reachable so the operator
# isn't left wondering why `ceebee` isn't found.
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo
    echo "Note: ${INSTALL_DIR} is not on your PATH."
    echo "Add it with:  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac
