# Runbook

## Local Smoke Test

```bash
KUBECONFIG=/path/to/kubeconfig go run ./cmd/sealos-collector \
  --config ./configs/sealos.example.yaml \
  --output ./dist/summary.json
```

The example includes an in-cluster VictoriaMetrics service URL. When running
from a laptop with only `KUBECONFIG`, that check may be `unknown`; run the
collector inside the cluster or use a port-forwarded Prometheus URL to verify
metrics checks locally.

Inspect:

```bash
jq '.overallStatus, .components[].status' ./dist/summary.json
```

## Test Suite

```bash
go test ./...
```

## Build Binary

```bash
go build -o ./bin/sealos-collector ./cmd/sealos-collector
go build -o ./bin/openstatus-sync ./cmd/openstatus-sync
```

## Build Container

```bash
docker buildx build --platform linux/amd64 -t sealos-collector:local .
```

## Host Deployment

A host deployment runs outside Kubernetes and keeps OpenStatus as the public UI:

- collector container: `sealos-collector`
- OpenStatus sync container: `sealos-openstatus-sync`
- OpenStatus status-page app container: `openstatus-status-page`
- OpenStatus status proxy container: `openstatus-status-proxy`
- backend snapshot file: `$REMOTE_ROOT/public/summary.json`
- default remote root: `/opt/sealos-collector`
- default OpenStatus root: `/opt/openstatus`

The host example config is `configs/host.example.yaml`. Copy it to an ignored
local file before adding private cluster values:

```bash
cp configs/host.example.yaml configs/my-cluster.local.yaml
```

Use a Prometheus-compatible URL reachable from the host. Host processes often
cannot resolve Kubernetes `*.svc` DNS names, so use a port-forward, load
balancer, or in-network proxy when needed.

Deploy the collector, OpenStatus dependency containers, and syncer:

```bash
REMOTE=status-host \
CONFIG_SOURCE=configs/my-cluster.local.yaml \
REMOTE_ROOT=/opt/sealos-collector \
OPENSTATUS_ROOT=/opt/openstatus \
KUBECONFIG_REMOTE_PATH=/etc/sealos-collector/kubeconfig \
CONSOLE_HOST=console.example.com \
CONSOLE_ADDR=console.example.com:443 \
PUBLIC_URL=http://status.example.com/ \
OPENSTATUS_PAGE_HOST=sealos-status.openstatus.dev \
OPENSTATUS_PAGE_SLUG=sealos-status \
OPENSTATUS_PAGE_TITLE="Sealos Status" \
deploy/host/deploy-openstatus-host.sh
```

The script builds linux/amd64 binaries, uploads them to the remote host,
refreshes the console certificate trust file, and recreates the OpenStatus
dependency containers plus `sealos-collector` and `sealos-openstatus-sync`. It
expects the OpenStatus env file to already exist at
`$OPENSTATUS_ROOT/.env.docker` on the remote host. Keep that env file private.

The manual equivalent is:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -o /tmp/sealos-collector-linux-amd64 ./cmd/sealos-collector
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -o /tmp/openstatus-sync-linux-amd64 ./cmd/openstatus-sync

ssh "$REMOTE" 'mkdir -p /opt/sealos-collector/{bin,config,public,logs}'
scp /tmp/sealos-collector-linux-amd64 \
  "$REMOTE":/opt/sealos-collector/bin/sealos-collector
scp /tmp/openstatus-sync-linux-amd64 \
  "$REMOTE":/opt/sealos-collector/bin/openstatus-sync
scp ./configs/my-cluster.local.yaml \
  "$REMOTE":/opt/sealos-collector/config/sealos.yaml
```

Trust the console ingress certificate for backend HTTPS checks:

```bash
ssh "$REMOTE" '
mkdir -p /opt/sealos-collector/certs
openssl s_client -showcerts -connect console.example.com:443 \
  -servername console.example.com </dev/null 2>/dev/null \
  | awk '\''/BEGIN CERTIFICATE/{print; in_cert=1; next} /END CERTIFICATE/{print; exit} in_cert{print}'\'' \
  > /opt/sealos-collector/certs/console.example.com.pem
test -s /opt/sealos-collector/certs/console.example.com.pem
'
```

Start or replace the runtime containers:

```bash
ssh "$REMOTE" '
docker rm -f sealos-collector sealos-openstatus-sync >/dev/null 2>&1 || true
docker run -d --name sealos-collector --restart unless-stopped --network host \
  -v /opt/sealos-collector/bin/sealos-collector:/sealos-collector:ro \
  -v /opt/sealos-collector/config:/config:ro \
  -v /etc/sealos-collector/kubeconfig:/etc/sealos-collector/kubeconfig:ro \
  -v /opt/sealos-collector/public:/public \
  -v /opt/sealos-collector/certs/console.example.com.pem:/certs/console.example.com.pem:ro \
  alpine:3.22 \
  /bin/sh -c "cat /etc/ssl/certs/ca-certificates.crt /certs/console.example.com.pem > /tmp/ca-bundle.crt && SSL_CERT_FILE=/tmp/ca-bundle.crt /sealos-collector --config /config/sealos.yaml --output /public/summary.json --state /public/state.json --interval 60s"

docker run -d --name sealos-openstatus-sync --restart unless-stopped \
  --network openstatus \
  -v /opt/sealos-collector/bin/openstatus-sync:/openstatus-sync:ro \
  -v /opt/sealos-collector/public:/public:ro \
  -e OPENSTATUS_DATABASE_URL=http://openstatus-libsql:8080 \
  alpine:3.22 \
  /openstatus-sync --snapshot /public/summary.json --page-slug sealos-status \
  --page-title "Sealos Status" --interval 60s --include-internal \
  --show-uptime=false
'
```

Mount the `public` directory, not the `summary.json` file. The collector writes
snapshots with atomic rename; a file-level Docker bind mount can keep
`openstatus-sync` pinned to an old inode. The same directory also holds
`state.json`, which lets the collector suppress transient `unknown` noise by
using recent last-known component evidence before marking a signal stale.

The self-hosted OpenStatus status-page image sends a production CSP with
`upgrade-insecure-requests`. When the dev page is served over plain HTTP at
the status host, browsers may upgrade `_next/static` CSS/JS requests to HTTPS
and render unstyled HTML. Keep the status page app internal and put the Nginx
proxy in front of it. The proxy also redirects `/monitors` and `/en/monitors`
back to `/`, hides the unused Monitors navigation link while uptime probes are
disabled, and applies compact spacing so public incident pages keep status-page
density.

```bash
sed 's/__OPENSTATUS_PAGE_HOST__/sealos-status.openstatus.dev/g' \
  ./deploy/host/openstatus-status-proxy.conf.template \
  > /tmp/status-proxy.conf
scp /tmp/status-proxy.conf "$REMOTE":/opt/openstatus/status-proxy.conf

ssh "$REMOTE" '
docker rm -f openstatus-status-proxy openstatus-status-page >/dev/null 2>&1 || true
docker run -d --name openstatus-status-page --restart unless-stopped \
  --network openstatus \
  --env-file /opt/openstatus/.env.docker \
  -e DATABASE_URL=http://openstatus-libsql:8080 \
  -e PORT=3000 \
  -e HOSTNAME=0.0.0.0 \
  -e AUTH_TRUST_HOST=true \
  ghcr.io/openstatushq/openstatus-status-page:latest

docker run -d --name openstatus-status-proxy --restart unless-stopped \
  --network openstatus \
  -p "$STATUS_PAGE_PORT":8080 \
  -v /opt/openstatus/status-proxy.conf:/etc/nginx/conf.d/default.conf:ro \
  nginx:1.27-alpine
'
```

Verify:

```bash
ssh "$REMOTE" 'docker logs --tail 20 sealos-openstatus-sync'
curl -sI http://status.example.com/
curl -s http://status.example.com/en/feed/json
curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' \
  http://status.example.com/monitors
curl -sI http://status.example.com/ | grep -i content-security-policy
```

OpenStatus components do not store a direct collector-owned current-status
column. The syncer reflects component health by creating an active status report
when a component is `degraded`, `outage`, or `unknown`, and resolving that
report when the component returns to `operational`. With `--show-uptime=false`,
the syncer maps collector components to OpenStatus static page components,
disables collector-owned monitor rows, and resolves stale reports for components
that are no longer managed by the collector. Enable `--show-uptime=true` only
after real OpenStatus monitor runs are available.

Status report updates are public-safe Incident Digests, not raw collector dumps.
The collector always emits sanitized `publicChecks` with structured
`reasonCode`, `impactHint`, `signalSummary`, and `confidence` fields;
`openstatus-sync` renders those fields into a concise headline cause, an impact
sentence, and at most two safe signals. Recovery stays one line. Keep
`publish.includeCheckDetails: false` for public pages unless a private consumer
explicitly needs raw check metadata.

`recentWarnings` is intentionally current-state biased. It counts recent
Warning events for active objects, but ignores warnings for deleted,
terminating, or completed Pods and the common Kubernetes retry message
`the object has been modified`. If the public page stays degraded, first check
whether the referenced object still exists before treating the event as an
active incident.

If a console endpoint uses a private or self-signed certificate, append that
certificate to the collector container CA bundle instead of disabling TLS
verification. TLS verification is part of the health signal.

## Public Snapshot Safety

Before publishing to a public bucket or CDN:

1. Set `publish.includeCheckDetails: false`.
2. Review component and group names.
3. Confirm no kubeconfig, token, secret, or internal credential appears in the
   generated JSON.
4. Ensure the public page handles stale `generatedAt` timestamps.

## Common Issues

- `kubernetes client unavailable`: set `KUBECONFIG` locally or run in cluster.
- `prometheus query failed`: verify the collector can reach the configured
  Prometheus/VictoriaMetrics service from its runtime environment.
- `workload not found`: check namespace, kind, and `resourceName` in config.
