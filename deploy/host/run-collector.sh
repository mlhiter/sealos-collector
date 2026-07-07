#!/usr/bin/env sh
set -eu

ROOT="${SEALOS_COLLECTOR_ROOT:-/opt/sealos-collector}"
INTERVAL="${SEALOS_COLLECTOR_INTERVAL:-60s}"
CONFIG_PATH="${SEALOS_COLLECTOR_CONFIG:-$ROOT/config/sealos.yaml}"

exec "$ROOT/bin/sealos-collector" \
  --config "$CONFIG_PATH" \
  --output "$ROOT/public/summary.json" \
  --state "$ROOT/public/state.json" \
  --interval "$INTERVAL"
