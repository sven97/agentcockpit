#!/usr/bin/env sh
# AgentCockpit host agent installer
# Usage: curl -fsSL agentcockpit.sh | sh
set -e

REPO="sven97/agentcockpit"
BIN_NAME="agentcockpit"
INSTALL_DIR="${AGENTCOCKPIT_INSTALL_DIR:-$HOME/.local/bin}"

# Detect platform
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

# Fetch latest release version
VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed 's/.*"v\([^"]*\)".*/\1/')"
if [ -z "$VERSION" ]; then
  echo "Failed to fetch latest version" >&2; exit 1
fi

URL="https://github.com/$REPO/releases/download/v${VERSION}/${BIN_NAME}_${VERSION}_${OS}_${ARCH}.tar.gz"

echo "Installing AgentCockpit v${VERSION} (${OS}/${ARCH})..."

# Download and extract
TMP="$(mktemp -d)"
curl -fsSL "$URL" | tar -xz -C "$TMP"

# Install binary
mkdir -p "$INSTALL_DIR"
mv "$TMP/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
chmod +x "$INSTALL_DIR/$BIN_NAME"
rm -rf "$TMP"

# Add to PATH if needed
if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
  SHELL_RC="$HOME/.zshrc"
  [ -f "$HOME/.bashrc" ] && SHELL_RC="$HOME/.bashrc"
  echo "export PATH=\"$INSTALL_DIR:\$PATH\"" >> "$SHELL_RC"
  export PATH="$INSTALL_DIR:$PATH"
  echo "Added $INSTALL_DIR to PATH in $SHELL_RC"
fi

echo ""
echo "AgentCockpit installed successfully."
echo "Run: agentcockpit connect"
