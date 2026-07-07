#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REMOTE="${REMOTE:-}"
REMOTE_ROOT="${REMOTE_ROOT:-/opt/sealos-collector}"
OPENSTATUS_ROOT="${OPENSTATUS_ROOT:-/opt/openstatus}"
CONFIG_SOURCE="${CONFIG_SOURCE:-$REPO_ROOT/configs/host.example.yaml}"
REMOTE_CONFIG_NAME="${REMOTE_CONFIG_NAME:-sealos.yaml}"
KUBECONFIG_REMOTE_PATH="${KUBECONFIG_REMOTE_PATH:-/etc/sealos-collector/kubeconfig}"
COLLECTOR_INTERVAL="${COLLECTOR_INTERVAL:-60s}"
OPENSTATUS_SYNC_INTERVAL="${OPENSTATUS_SYNC_INTERVAL:-60s}"
CONSOLE_HOST="${CONSOLE_HOST:-console.example.com}"
CONSOLE_ADDR="${CONSOLE_ADDR:-console.example.com:443}"
PUBLIC_URL="${PUBLIC_URL:-http://status.example.com/}"
OPENSTATUS_PAGE_HOST="${OPENSTATUS_PAGE_HOST:-sealos-status.openstatus.dev}"
OPENSTATUS_WORKSPACE_SLUG="${OPENSTATUS_WORKSPACE_SLUG:-sealos}"
OPENSTATUS_WORKSPACE_NAME="${OPENSTATUS_WORKSPACE_NAME:-Sealos}"
OPENSTATUS_PAGE_SLUG="${OPENSTATUS_PAGE_SLUG:-sealos-status}"
OPENSTATUS_PAGE_TITLE="${OPENSTATUS_PAGE_TITLE:-Sealos Status}"
OPENSTATUS_PAGE_DESCRIPTION="${OPENSTATUS_PAGE_DESCRIPTION:-Sealos platform health collected from read-only cluster evidence.}"
DASHBOARD_PORT="${DASHBOARD_PORT:-13002}"
STATUS_PAGE_PORT="${STATUS_PAGE_PORT:-13003}"

if [ -z "$REMOTE" ]; then
  echo "REMOTE is required, for example: REMOTE=status-host deploy/host/deploy-openstatus-host.sh" >&2
  exit 1
fi
if [ ! -f "$CONFIG_SOURCE" ]; then
  echo "CONFIG_SOURCE does not exist: $CONFIG_SOURCE" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

sed "s/__OPENSTATUS_PAGE_HOST__/$OPENSTATUS_PAGE_HOST/g" \
  "$REPO_ROOT/deploy/host/openstatus-status-proxy.conf.template" \
  > "$tmpdir/status-proxy.conf"

echo "Building linux/amd64 binaries..."
(
  cd "$REPO_ROOT"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$tmpdir/sealos-collector" ./cmd/sealos-collector
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$tmpdir/openstatus-sync" ./cmd/openstatus-sync
)

echo "Preparing remote directories on $REMOTE..."
ssh "$REMOTE" "set -euo pipefail
mkdir -p '$REMOTE_ROOT'/bin '$REMOTE_ROOT'/config '$REMOTE_ROOT'/public '$REMOTE_ROOT'/certs '$REMOTE_ROOT'/backups '$OPENSTATUS_ROOT'
test -f '$OPENSTATUS_ROOT/.env.docker' || {
  echo 'missing $OPENSTATUS_ROOT/.env.docker; create it before deploying OpenStatus' >&2
  exit 1
}
"

echo "Uploading binaries and deployment config..."
scp "$tmpdir/sealos-collector" "$REMOTE:$REMOTE_ROOT/bin/sealos-collector.new"
scp "$tmpdir/openstatus-sync" "$REMOTE:$REMOTE_ROOT/bin/openstatus-sync.new"
scp "$CONFIG_SOURCE" "$REMOTE:$REMOTE_ROOT/config/$REMOTE_CONFIG_NAME"
scp "$tmpdir/status-proxy.conf" "$REMOTE:$OPENSTATUS_ROOT/status-proxy.conf"

echo "Deploying OpenStatus and collector runtime..."
ssh "$REMOTE" "set -euo pipefail
cd '$REMOTE_ROOT'
stamp=\$(date -u +%Y%m%d%H%M%S)
if [ -x bin/sealos-collector ]; then cp bin/sealos-collector backups/sealos-collector.\$stamp; fi
if [ -x bin/openstatus-sync ]; then cp bin/openstatus-sync backups/openstatus-sync.\$stamp; fi
mv bin/sealos-collector.new bin/sealos-collector
mv bin/openstatus-sync.new bin/openstatus-sync
chmod +x bin/sealos-collector bin/openstatus-sync

openssl s_client -showcerts -connect '$CONSOLE_ADDR' -servername '$CONSOLE_HOST' </dev/null 2>/dev/null \
  | awk '/BEGIN CERTIFICATE/{print; in_cert=1; next} /END CERTIFICATE/{print; exit} in_cert{print}' \
  > certs/$CONSOLE_HOST.pem
test -s certs/$CONSOLE_HOST.pem

docker network create openstatus >/dev/null 2>&1 || true

docker rm -f openstatus-libsql openstatus-dashboard openstatus-status-page openstatus-status-proxy >/dev/null 2>&1 || true
docker run -d --name openstatus-libsql --restart unless-stopped \
  --network openstatus \
  -v openstatus-libsql-data:/var/lib/sqld \
  ghcr.io/tursodatabase/libsql-server:latest

docker run -d --name openstatus-dashboard --restart unless-stopped \
  --network openstatus \
  -p $DASHBOARD_PORT:3000 \
  --env-file '$OPENSTATUS_ROOT/.env.docker' \
  -e DATABASE_URL=http://openstatus-libsql:8080 \
  -e PORT=3000 \
  -e HOSTNAME=0.0.0.0 \
  -e AUTH_TRUST_HOST=true \
  ghcr.io/openstatushq/openstatus-dashboard:latest

docker run -d --name openstatus-status-page --restart unless-stopped \
  --network openstatus \
  --env-file '$OPENSTATUS_ROOT/.env.docker' \
  -e DATABASE_URL=http://openstatus-libsql:8080 \
  -e PORT=3000 \
  -e HOSTNAME=0.0.0.0 \
  -e AUTH_TRUST_HOST=true \
  ghcr.io/openstatushq/openstatus-status-page:latest

docker run -d --name openstatus-status-proxy --restart unless-stopped \
  --network openstatus \
  -p $STATUS_PAGE_PORT:8080 \
  -v '$OPENSTATUS_ROOT/status-proxy.conf:/etc/nginx/conf.d/default.conf:ro' \
  nginx:1.27-alpine

docker rm -f sealos-collector sealos-openstatus-sync >/dev/null 2>&1 || true
docker run -d --name sealos-collector --restart unless-stopped --network host \
  -v '$REMOTE_ROOT/bin/sealos-collector:/sealos-collector:ro' \
  -v '$REMOTE_ROOT/config:/config:ro' \
  -v '$KUBECONFIG_REMOTE_PATH:$KUBECONFIG_REMOTE_PATH:ro' \
  -v '$REMOTE_ROOT/public:/public' \
  -v '$REMOTE_ROOT/certs/$CONSOLE_HOST.pem:/certs/$CONSOLE_HOST.pem:ro' \
  alpine:3.22 \
  /bin/sh -c 'cat /etc/ssl/certs/ca-certificates.crt /certs/$CONSOLE_HOST.pem > /tmp/ca-bundle.crt && SSL_CERT_FILE=/tmp/ca-bundle.crt /sealos-collector --config /config/$REMOTE_CONFIG_NAME --output /public/summary.json --state /public/state.json --interval $COLLECTOR_INTERVAL'

docker run -d --name sealos-openstatus-sync --restart unless-stopped \
  --network openstatus \
  -v '$REMOTE_ROOT/bin/openstatus-sync:/openstatus-sync:ro' \
  -v '$REMOTE_ROOT/public:/public:ro' \
  alpine:3.22 \
  /openstatus-sync --snapshot /public/summary.json \
  --database-url http://openstatus-libsql:8080 \
  --workspace-slug '$OPENSTATUS_WORKSPACE_SLUG' \
  --workspace-name '$OPENSTATUS_WORKSPACE_NAME' \
  --page-slug '$OPENSTATUS_PAGE_SLUG' \
  --page-title '$OPENSTATUS_PAGE_TITLE' \
  --page-description '$OPENSTATUS_PAGE_DESCRIPTION' \
  --interval $OPENSTATUS_SYNC_INTERVAL \
  --show-uptime=false \
  --include-internal

docker ps --format '{{.Names}} {{.Status}}' | grep -E 'openstatus|sealos'
"

echo "Verifying public page..."
curl -sS -o /dev/null -w "root=%{http_code}\n" "$PUBLIC_URL"
curl -sS -o /dev/null -w "feed=%{http_code}\n" "${PUBLIC_URL%/}/en/feed/json"
curl -sS -o /dev/null -w "monitors=%{http_code} redirect=%{redirect_url}\n" "${PUBLIC_URL%/}/monitors"
