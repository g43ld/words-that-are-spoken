#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUTPUT_DIR="$ROOT_DIR/dist/linux"
EXECUTABLE="$OUTPUT_DIR/wtas"

if ! command -v go >/dev/null 2>&1; then
  echo "Go was not found on PATH. Install Go 1.22 or newer and reopen the terminal." >&2
  exit 1
fi

for required_path in "$ROOT_DIR/go.mod" "$ROOT_DIR/bin" "$ROOT_DIR/models"; do
  if [[ ! -e "$required_path" ]]; then
    echo "Required path not found: $required_path" >&2
    exit 1
  fi
done

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o "$EXECUTABLE" "$ROOT_DIR"

cp -R "$ROOT_DIR/bin" "$OUTPUT_DIR/"
cp -R "$ROOT_DIR/models" "$OUTPUT_DIR/"
chmod +x "$EXECUTABLE"

echo "Linux package created: $OUTPUT_DIR"
