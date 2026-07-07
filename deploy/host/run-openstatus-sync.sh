#!/usr/bin/env sh
set -eu

ROOT="${SEALOS_COLLECTOR_ROOT:-/opt/sealos-collector}"
INTERVAL="${OPENSTATUS_SYNC_INTERVAL:-60s}"
DATABASE_URL="${OPENSTATUS_DATABASE_URL:-http://openstatus-libsql:8080}"

exec "$ROOT/bin/openstatus-sync" \
  --snapshot "$ROOT/public/summary.json" \
  --database-url "$DATABASE_URL" \
  --workspace-slug "${OPENSTATUS_WORKSPACE_SLUG:-sealos}" \
  --workspace-name "${OPENSTATUS_WORKSPACE_NAME:-Sealos}" \
  --page-slug "${OPENSTATUS_PAGE_SLUG:-sealos-status}" \
  --page-title "${OPENSTATUS_PAGE_TITLE:-Sealos Status}" \
  --page-description "${OPENSTATUS_PAGE_DESCRIPTION:-Sealos platform health collected from read-only cluster evidence.}" \
  --interval "$INTERVAL" \
  --show-uptime=false \
  --include-internal
