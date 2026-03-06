#!/usr/bin/env bash
set -euo pipefail

# If a workspace .local/.share/opencode directory exists, symlink it into HOME
# Resolve project root relative to this script (one level up)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SRC_DIR="$PROJECT_ROOT/.local/.share/opencode"
DEST_DIR="$HOME/.local/share/opencode"

if [ -d "$SRC_DIR" ]; then
  mkdir -p "$(dirname "$DEST_DIR")"
  ln -sfn "$SRC_DIR" "$DEST_DIR"
fi

# If the mounted auth file exists, link it only when no auth.json is present
MOUNTED=/mnt/opencode-auth.json
DEST_AUTH="$DEST_DIR/auth.json"

if [ -e "$MOUNTED" ]; then
  if [ ! -e "$DEST_AUTH" ]; then
    mkdir -p "$(dirname "$DEST_AUTH")"
    ln -sf "$MOUNTED" "$DEST_AUTH"
  fi
fi

exit 0
