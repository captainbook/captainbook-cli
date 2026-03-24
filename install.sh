#!/bin/sh
set -e

REPO="captainbook/captainbook-cli"
INSTALL_DIR="/usr/local/bin"
BINARY="ceebee"

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
EXPECTED="$(curl -fsSL "$CHECKSUMS_URL" | grep "  ${ASSET}$" | awk '{print $1}')"
if [ -n "$EXPECTED" ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL="$(sha256sum "$TMPFILE" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL="$(shasum -a 256 "$TMPFILE" | awk '{print $1}')"
  else
    ACTUAL=""
    echo "Warning: no sha256 tool found, skipping checksum verification" >&2
  fi
  if [ -n "$ACTUAL" ]; then
    if [ "$ACTUAL" != "$EXPECTED" ]; then
      echo "Checksum mismatch!" >&2
      echo "  Expected: ${EXPECTED}" >&2
      echo "  Got:      ${ACTUAL}" >&2
      exit 1
    fi
    echo "Checksum verified."
  fi
else
  echo "Warning: checksums not available, skipping verification" >&2
fi

# Install
chmod +x "$TMPFILE"
if [ -w "$INSTALL_DIR" ]; then
  mv "$TMPFILE" "${INSTALL_DIR}/${BINARY}"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mv "$TMPFILE" "${INSTALL_DIR}/${BINARY}"
fi

echo "Installed ${BINARY} ${TAG} to ${INSTALL_DIR}/${BINARY}"
