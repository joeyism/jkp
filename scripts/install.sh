#!/bin/sh
# scripts/install.sh — install jkp from a release URL.
#
# No Go toolchain required. Downloads a prebuilt binary and the slash-command
# files, verifies SHA-256, installs to ~/.local/bin.
#
# Environment variables:
#   JKP_TARGET        — override platform detection (e.g. linux-amd64)
#   JKP_RELEASE_BASE  — URL containing the assets (default: GitHub v0.1.0)
#   JKP_PREFIX        — install prefix (default: $HOME/.local)
#
# Exits 0 on success. Exits 2 if the binary is not on PATH (still installed).
# Exits 1 on error (bad platform, download failure, checksum mismatch).

set -eu

TARGET="${JKP_TARGET:-}"
BASE="${JKP_RELEASE_BASE:-https://github.com/joeyism/jkp/releases/download/v0.1.0}"
BASE="${BASE%/}"  # strip trailing slash
PREFIX="${JKP_PREFIX:-$HOME/.local}"
BIN_DIR="$PREFIX/bin"

# --- platform detection ---
if [ -z "$TARGET" ]; then
  OS="$(uname -s)"
  ARCH="$(uname -m)"
  case "$OS" in
    Darwin)  TARGET_OS="darwin" ;;
    Linux)   TARGET_OS="linux"  ;;
    *)       echo "jkp: unsupported OS: $OS" >&2; exit 1 ;;
  esac
  case "$ARCH" in
    arm64|aarch64) TARGET_ARCH="arm64" ;;
    x86_64)        TARGET_ARCH="amd64" ;;
    *)             echo "jkp: unsupported arch: $ARCH" >&2; exit 1 ;;
  esac
  TARGET="${TARGET_OS}-${TARGET_ARCH}"
fi

ASSET="jkp-$TARGET"
echo "jkp: downloading $ASSET from $BASE"

# --- download to temp dir ---
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if ! curl -fsSL "$BASE/$ASSET" -o "$TMP/$ASSET"; then
  echo "jkp: failed to download $BASE/$ASSET" >&2
  rm -f "$TMP/$ASSET"
  exit 1
fi

if ! curl -fsSL "$BASE/SHA256SUMS" -o "$TMP/SHA256SUMS"; then
  echo "jkp: failed to download SHA256SUMS" >&2
  exit 1
fi

# --- verify checksum ---
# Format: <64-hex>  <filename>  (two spaces, sha256sum default)
EXPECTED=$(awk -v f="$ASSET" '$2 == f {print $1}' "$TMP/SHA256SUMS")
[ -n "$EXPECTED" ] || { echo "jkp: no checksum for $ASSET in SHA256SUMS" >&2; exit 1; }

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "$TMP/$ASSET" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "$TMP/$ASSET" | awk '{print $1}')
else
  echo "jkp: sha256sum or shasum required" >&2
  exit 1
fi

if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "jkp: checksum mismatch for $ASSET" >&2
  rm -f "$TMP/$ASSET"
  exit 1
fi
echo "jkp: checksum verified"

# --- install binary ---
mkdir -p "$BIN_DIR"
cp "$TMP/$ASSET" "$BIN_DIR/jkp"
chmod 0755 "$BIN_DIR/jkp"
echo "jkp: installed $BIN_DIR/jkp"

# --- install slash-command files ---
HOOKS="$HOME/.config/opencode/command $HOME/.config/opencode/skills/jkp $HOME/.claude/commands $HOME/.pi/agent/extensions"
for hook in "$HOME/.config/opencode/command/jkp.md" "$HOME/.config/opencode/skills/jkp/SKILL.md" "$HOME/.claude/commands/jkp.md" "$HOME/.pi/agent/extensions/jkp.ts"; do
  dir="$(dirname "$hook")"
  mkdir -p "$dir"
done

for name in opencode-jkp.md opencode-jkp-skill.md claude-jkp.md pi-jkp.ts; do
  if ! curl -fsSL "$BASE/$name" -o "$TMP/$name"; then
    echo "jkp: warning: $name not found at $BASE (skipping)" >&2
    continue
  fi
  case "$name" in
    opencode-jkp.md) cp "$TMP/$name" "$HOME/.config/opencode/command/jkp.md" ;;
    opencode-jkp-skill.md) cp "$TMP/$name" "$HOME/.config/opencode/skills/jkp/SKILL.md" ;;
    claude-jkp.md)   cp "$TMP/$name" "$HOME/.claude/commands/jkp.md" ;;
    pi-jkp.ts)       cp "$TMP/$name" "$HOME/.pi/agent/extensions/jkp.ts" ;;
  esac
  echo "jkp: installed $name"
done

# --- PATH check ---
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    echo "jkp: installed, but $BIN_DIR is not on PATH."
    echo "export PATH=\"$BIN_DIR:\$PATH\""
    exit 2
    ;;
esac

echo "jkp: done"
