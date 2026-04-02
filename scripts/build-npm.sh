#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
NPM_DIR="$CLI_DIR/npm"

echo "Building vibecast binaries..."

# Build matrix: GOOS GOARCH npm-package-dir
targets=(
  "linux   amd64  linux-x64"
  "linux   arm64  linux-arm64"
  "darwin  amd64  darwin-x64"
  "darwin  arm64  darwin-arm64"
)

for target in "${targets[@]}"; do
  read -r goos goarch dir <<< "$target"
  echo "  Compiling ${goos}/${goarch} → npm/${dir}/bin/vibecast"

  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
    go build -C "$CLI_DIR" -o "$NPM_DIR/$dir/bin/vibecast" .

  chmod +x "$NPM_DIR/$dir/bin/vibecast"

  # Copy the claude-plugin directory so PluginDir() finds it next to the binary
  rm -rf "$NPM_DIR/$dir/bin/claude-plugin"
  cp -r "$CLI_DIR/claude-plugin" "$NPM_DIR/$dir/bin/claude-plugin"
done

echo ""
echo "All binaries built successfully."
echo ""
echo "To publish:"
echo "  cd $NPM_DIR/linux-x64    && npm publish --access public"
echo "  cd $NPM_DIR/linux-arm64  && npm publish --access public"
echo "  cd $NPM_DIR/darwin-x64   && npm publish --access public"
echo "  cd $NPM_DIR/darwin-arm64 && npm publish --access public"
echo "  cd $NPM_DIR/vibecast     && npm publish"
