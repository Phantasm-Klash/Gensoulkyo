#!/usr/bin/env sh
# Build the Gensoulkyo Nakama Go runtime plugin (gensoulkyo.so).
#
# The plugin is built inside the heroiclabs/nakama-pluginbuilder image so its
# Go ABI matches the heroiclabs/nakama server image. Run this from anywhere in
# the workspace; it locates the workspace root (the directory that contains
# both Gensoulkyo/ and PhK-Protocol/).
#
# Output: Gensoulkyo/deployments/nakama/modules/gensoulkyo.so
set -eu

NAKAMA_VERSION="${NAKAMA_VERSION:-3.41.0}"
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
OUT_DIR="$SCRIPT_DIR/modules"

if [ ! -d "$ROOT/Gensoulkyo" ] || [ ! -d "$ROOT/PhK-Protocol" ]; then
  echo "error: workspace root not found at $ROOT (need Gensoulkyo/ and PhK-Protocol/)" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"

echo "building gensoulkyo.so with nakama-pluginbuilder:${NAKAMA_VERSION}"

docker run --rm \
  --entrypoint /bin/sh \
  -v "$ROOT:/build" \
  -w /build/Gensoulkyo \
  -e GOPROXY="$GOPROXY" \
  -e GOSUMDB=off \
  "heroiclabs/nakama-pluginbuilder:${NAKAMA_VERSION}" \
  -ec "go build -tags nakama -trimpath -buildmode=plugin \
       -o /build/Gensoulkyo/deployments/nakama/modules/gensoulkyo.so \
       ./cmd/gensoulkyo_nakama"

echo "ok: $OUT_DIR/gensoulkyo.so"
ls -la "$OUT_DIR/gensoulkyo.so"
