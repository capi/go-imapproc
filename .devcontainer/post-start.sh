#!/usr/bin/env bash
set -euo pipefail

# If the mounted file exists, ensure parent dir exists and symlink it into HOME
MOUNTED=/mnt/opencode-auth.json
DEST="$HOME/.local/share/opencode/auth.json"
DESTDIR=$(dirname "$DEST")

if [ -e "$MOUNTED" ]; then
  mkdir -p "$DESTDIR"
  ln -sf "$MOUNTED" "$DEST"
fi

exit 0
