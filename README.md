# Sealos Collector

Sealos Collector is a backend health evidence adapter for OpenStatus-backed
public status pages. It collects platform signals from a Sealos/Kubernetes
cluster, emits a sanitized JSON snapshot, and can sync that snapshot into
OpenStatus page components and status reports.

The project does not own the public UI. OpenStatus is the user-facing status
page; this project only collects and adapts Sealos-specific platform evidence.

## What It Collects

- Kubernetes control-plane readiness via `/readyz`
- Deployment, StatefulSet, and DaemonSet readiness
- Service endpoint address availability through EndpointSlices
- HTTP checks from the collector runtime
- Prometheus/VictoriaMetrics instant queries
- Recent actionable Kubernetes Warning events; warnings attached to deleted,
  terminating, or completed Pods and common controller retry conflicts are
  ignored so historical event residue does not keep a public page degraded.

## What It Does Not Do Yet

- It does not host a custom public status page.
- It does not send subscriber notifications.
- It does not write to the monitored Kubernetes cluster.
- It writes only the OpenStatus tables needed for pages, page components,
  component groups, and status report lifecycle. OpenStatus monitor rows are
  used only when uptime probes are explicitly enabled.
- It does not replace OpenStatus dashboard/status-page UI.

## Quick Start

```bash
go run ./cmd/sealos-collector \
  --config ./configs/sealos.example.yaml \
  --output ./dist/summary.json
```

For local testing against a kubeconfig:

```bash
KUBECONFIG=/path/to/kubeconfig go run ./cmd/sealos-collector \
  --config ./configs/sealos.example.yaml \
  --output -
```

Run continuously:

```bash
go run ./cmd/sealos-collector \
  --config ./configs/sealos.example.yaml \
  --output ./dist/summary.json \
  --interval 60s
```

Sync a generated snapshot into OpenStatus:

```bash
OPENSTATUS_DATABASE_URL=http://openstatus-libsql:8080 \
go run ./cmd/openstatus-sync \
  --snapshot ./dist/summary.json \
  --page-slug sealos-status \
  --show-uptime=false \
  --interval 60s
```

## Snapshot Contract

The collector outputs a JSON document shaped for public status consumers:

```json
{
  "version": "v1",
  "cluster": { "id": "example-cluster", "name": "Example Sealos Cluster" },
  "generatedAt": "2026-07-07T10:00:00Z",
  "overallStatus": "operational",
  "components": [
    {
      "id": "console",
      "name": "Console",
      "group": "Access",
      "status": "operational",
      "summary": "All checks passed",
      "publicChecks": [
        {
          "id": "console-http",
          "name": "Console external HTTP",
          "type": "http",
          "status": "operational",
          "message": "http returned 200",
          "signalSummary": "Console external HTTP HTTP 200",
          "confidence": "measurement",
          "metadata": {
            "scheme": "https",
            "host": "console.example.com",
            "statusCode": "200"
          }
        }
      ]
    }
  ]
}
```

`publish.includeCheckDetails` defaults to the public-safe posture in the example
config: raw `checks` are hidden, while sanitized `publicChecks` remain available
so OpenStatus events can explain impact and failing checks without exposing
secrets, kubeconfigs, bearer tokens, raw request headers, or full internal
queries. Set `includeCheckDetails` to `true` only when the downstream consumer
is private or when you need debugging evidence.

`publicChecks` also carry structured incident semantics. Non-operational checks
include `reasonCode`, `impactHint`, `signalSummary`, and `confidence` so
OpenStatus can render dense Incident Digest updates without parsing raw log
or event samples. Warning event samples are classified inside the collector and
are not published in `publicChecks.metadata`.

## Architecture

```text
Kubernetes API / VictoriaMetrics / HTTP checks / Events
                    |
                    v
          sealos-collector snapshot
                    |
                    v
          openstatus-sync adapter
                    |
                    v
              OpenStatus status page
```

## Repository Layout

- `cmd/sealos-collector`: CLI entrypoint.
- `cmd/openstatus-sync`: backend adapter that writes snapshots into OpenStatus.
- `internal/config`: YAML configuration loading and validation.
- `internal/collector`: check execution and snapshot assembly.
- `internal/openstatus`: libSQL/Hrana client and OpenStatus sync logic.
- `internal/status`: public snapshot schema and status aggregation helpers.
- `configs/sealos.example.yaml`: example Sealos component mapping.
- `configs/host.example.yaml`: host deployment example. Copy it to an ignored
  `configs/*.local.yaml` file for private cluster values.
- `deploy/`: read-only Kubernetes RBAC and a CronJob smoke example. The sample
  CronJob writes JSON to logs; durable external publishing is a later phase.
- `deploy/host`: host/container deployment helpers for collector and
  OpenStatus deployment.

## Verification

```bash
go test ./...
```

## Host Deployment With OpenStatus

The host deployment helper runs the collector beside a self-hosted OpenStatus
stack. Keep private values in environment variables and ignored local config
files:

```bash
cp configs/host.example.yaml configs/my-cluster.local.yaml

REMOTE=status-host \
CONFIG_SOURCE=configs/my-cluster.local.yaml \
KUBECONFIG_REMOTE_PATH=/etc/sealos-collector/kubeconfig \
CONSOLE_HOST=console.example.com \
CONSOLE_ADDR=console.example.com:443 \
PUBLIC_URL=http://status.example.com/ \
OPENSTATUS_PAGE_HOST=sealos-status.openstatus.dev \
deploy/host/deploy-openstatus-host.sh
```

`sealos-collector` writes `summary.json` into the host `public` directory.
`openstatus-sync` reads that mounted directory and updates OpenStatus static
page components plus active/resolved status reports. Collector-owned status
reports remain the source of truth for component health. OpenStatus
uptime/monitor rows are disabled by default so the public page does not show
empty monitor data.

The host proxy presents the status page with the configured OpenStatus slug
host, strips the self-hosted status-page CSP directive that forces HTTP
deployments to upgrade static assets to HTTPS, redirects `/monitors` back to
`/` while uptime is hidden, and keeps incident pages compact.
