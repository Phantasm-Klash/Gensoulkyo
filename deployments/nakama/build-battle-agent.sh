#!/usr/bin/env sh
# Build the Gensoulkyo battle-agent (remote battle server supervisor).
#
# The agent is standard-library only, but it shares Gensoulkyo/go.mod (go 1.27.1),
# so it is built inside the same heroiclabs/nakama-pluginbuilder image used for
# the runtime plugin. That keeps a single pinned Go toolchain for the whole
# deployment and avoids downloading a toolchain on hosts with a blocked proxy.
#
# Output: Gensoulkyo/deployments/nakama/modules/battle-agent
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

echo "building battle-agent with nakama-pluginbuilder:${NAKAMA_VERSION}"

docker run --rm \
  --entrypoint /bin/sh \
  -v "$ROOT:/build" \
  -w /build/Gensoulkyo \
  -e GOPROXY="$GOPROXY" \
  -e GOSUMDB=off \
  -e CGO_ENABLED=0 \
  "heroiclabs/nakama-pluginbuilder:${NAKAMA_VERSION}" \
  -ec "go vet ./cmd/battle-agent/ && \
       go build -trimpath -o /build/Gensoulkyo/deployments/nakama/modules/battle-agent ./cmd/battle-agent"

echo "ok: $OUT_DIR/battle-agent"
ls -la "$OUT_DIR/battle-agent"
