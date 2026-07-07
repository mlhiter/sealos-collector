# Sealos Collector

> Read-only Kubernetes health evidence for OpenStatus public status pages.

[![Go](https://img.shields.io/badge/Go-1.23-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-read--only-326CE5?style=flat-square&logo=kubernetes&logoColor=white)](https://kubernetes.io/)
[![OpenStatus](https://img.shields.io/badge/OpenStatus-adapter-111827?style=flat-square)](https://www.openstatus.dev/)

Sealos Collector turns internal platform signals from a Sealos-like Kubernetes
cluster into a sanitized status snapshot, then optionally syncs that snapshot
into [OpenStatus](https://www.openstatus.dev/) page components and incident
reports.

It is not a replacement for OpenStatus, Grafana, or alerting. It is the
thin evidence layer between Kubernetes reality and a public status page.

> [!IMPORTANT]
> The collector is read-only toward the monitored Kubernetes cluster. Public
> snapshots are designed to hide raw check internals by default.

[Quick start](#quick-start) • [How it works](#how-it-works) •
[Configuration](#configuration) • [OpenStatus sync](#openstatus-sync) •
[Deployment](#deployment) • [Docs](#docs)

## What it does

- Collects Kubernetes readiness, workloads, EndpointSlices, HTTP checks,
  Prometheus/VictoriaMetrics queries, and recent actionable Warning events.
- Aggregates those checks into user-facing component statuses:
  `operational`, `unknown`, `degraded`, or `outage`.
- Emits a public-safe `summary.json` snapshot with sanitized check evidence.
- Writes OpenStatus static page components and collector-owned status reports.
- Keeps OpenStatus uptime monitors disabled by default until real monitor data
  exists.

## What it does not do

- It does not host a custom public status page.
- It does not remediate incidents or write Kubernetes resources.
- It does not send subscriber notifications.
- It does not publish kubeconfigs, bearer tokens, raw headers, raw Secret data,
  full internal queries, or raw Warning samples.

## Quick start

### Prerequisites

- Go 1.23+
- Access to a Kubernetes cluster through `KUBECONFIG` or in-cluster config
- Optional: a Prometheus-compatible query endpoint
- Optional: a self-hosted OpenStatus libSQL HTTP endpoint

Run one collection pass with the example config:

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

Run continuously and keep lightweight state for transient `unknown` checks:

```bash
go run ./cmd/sealos-collector \
  --config ./configs/sealos.example.yaml \
  --output ./dist/summary.json \
  --state ./dist/state.json \
  --interval 60s
```

Inspect the result:

```bash
jq '{overallStatus, components: [.components[] | {id, status, summary}]}' \
  ./dist/summary.json
```

> [!NOTE]
> The default example includes an in-cluster VictoriaMetrics service URL. If you
> run from a laptop, either port-forward a Prometheus endpoint or remove that
> check for the first smoke test.

## How it works

```text
Kubernetes API / HTTP / Prometheus / Events
                  |
                  v
          sealos-collector
                  |
                  v
          sanitized summary.json
                  |
                  v
          openstatus-sync
                  |
                  v
       OpenStatus public status page
```

The collector maps platform evidence to product promises. A component without
evidence is `unknown`; a component with partial failure is `degraded`; a
component with no viable serving path is `outage`.

Non-operational checks include structured public semantics such as
`reasonCode`, `impactHint`, `signalSummary`, and `confidence`. The OpenStatus
adapter renders those fields into compact Incident Digest updates without
parsing raw Kubernetes event text.

## Snapshot format

The collector writes a JSON document shaped for public status consumers:

```json
{
  "version": "v1",
  "cluster": {
    "id": "example-cluster",
    "name": "Example Sealos Cluster"
  },
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
          "impact": "servingPath",
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

`publish.includeCheckDetails` defaults to `false` in the examples. Keep it that
way for public pages; set it to `true` only for trusted internal debugging.

## Configuration

Start from one of the examples:

| File | Use case |
| --- | --- |
| `configs/sealos.example.yaml` | In-cluster or local smoke testing with common Sealos-style components. |
| `configs/host.example.yaml` | Host/container deployment beside a self-hosted OpenStatus stack. |

Private values should live in ignored local config files:

```bash
cp configs/host.example.yaml configs/my-cluster.local.yaml
```

The collector resolves Kubernetes access in this order:

1. `cluster.kubeconfig` from the config file
2. `KUBECONFIG`
3. in-cluster config
4. local `~/.kube/config`

Supported check types include:

| Type | What it verifies |
| --- | --- |
| `workload` | Deployment, StatefulSet, or DaemonSet readiness. |
| `serviceEndpoints` | Ready EndpointSlice addresses for a Service. |
| `kubernetesReadyz` | Kubernetes API readiness endpoint. |
| `http` | External or internal HTTP status checks. |
| `prometheusQuery` | Prometheus-compatible instant query thresholds. |
| `recentWarnings` | Current actionable Kubernetes Warning events. |

Each check may also set an `impact` value. This does not change the raw check
result; it tells the collector how that raw signal should affect the
user-facing component status:

| Impact | Use for | Component effect |
| --- | --- | --- |
| `servingPath` | Entry points and data paths required for users to use the product. | Raw outage remains `outage`. |
| `controlPlane` | Controllers and management paths used to create or change product resources. | Raw outage becomes `degraded`. |
| `dependency` | Supporting services that can degrade the product but are not the direct entry point. | Raw outage becomes `degraded`. |
| `symptom` | Warning events and derived metrics that indicate risk rather than direct failure. | Raw outage becomes `degraded`. |
| `informational` | Evidence that should be published but should not affect the component status. | Does not degrade the component. |

When `impact` is omitted, the collector preserves legacy behavior and uses the
raw check status directly. The host example classifies the full Sealos product
profile so public status reflects user-facing product promises rather than only
internal object readiness.

## OpenStatus sync

Generate a snapshot, then sync it into OpenStatus:

```bash
OPENSTATUS_DATABASE_URL=http://openstatus-libsql:8080 \
go run ./cmd/openstatus-sync \
  --snapshot ./dist/summary.json \
  --workspace-slug sealos \
  --workspace-name "Sealos" \
  --page-slug sealos-status \
  --page-title "Sealos Status" \
  --show-uptime=false
```

With `--show-uptime=false`, the adapter uses static page components and keeps
the unused Monitors page out of the public status experience. Enable uptime
only after OpenStatus monitor runs are actually available.

OpenStatus reports are collector-owned:

- `operational` resolves the active report.
- `unknown` and `degraded` create or update a degraded-performance report.
- `outage` creates or updates a major-outage report.
- removed components have their stale collector-owned reports resolved.

## Deployment

There are two deployment shapes in this repository:

| Path | Purpose |
| --- | --- |
| `deploy/cronjob.yaml` | Read-only Kubernetes CronJob smoke example that writes snapshots to logs. |
| `deploy/host/deploy-openstatus-host.sh` | Host deployment helper for collector + OpenStatus sync + status-page proxy. |

For host deployment, keep real cluster values out of Git:

```bash
REMOTE=status-host \
CONFIG_SOURCE=configs/my-cluster.local.yaml \
KUBECONFIG_REMOTE_PATH=/etc/sealos-collector/kubeconfig \
CONSOLE_HOST=console.example.com \
CONSOLE_ADDR=console.example.com:443 \
PUBLIC_URL=http://status.example.com/ \
OPENSTATUS_PAGE_HOST=sealos-status.openstatus.dev \
deploy/host/deploy-openstatus-host.sh
```

See [docs/runbook.md](./docs/runbook.md) for container names, certificate
trust, OpenStatus proxy behavior, and smoke-test commands.

## Public safety checklist

Before publishing a snapshot or opening a status page:

- Keep `publish.includeCheckDetails: false`.
- Review component and group names for public readability.
- Verify generated JSON contains no kubeconfig, token, URL userinfo, raw Secret
  data, internal request headers, or full internal queries.
- Treat `summary.json` as a backend exchange file unless you explicitly publish
  it through a trusted route.
- Keep private cluster config in `configs/*.local.yaml`.

## Project layout

```text
cmd/sealos-collector    Collects evidence and writes summary.json
cmd/openstatus-sync     Syncs snapshots into OpenStatus
configs/                Public-safe example configs
deploy/                 Kubernetes and host deployment examples
docs/                   Architecture, runbook, references, IA
internal/collector      Check execution and aggregation
internal/openstatus     OpenStatus libSQL/Hrana adapter
internal/status         Public snapshot schema
```

## Development

Run the test suite:

```bash
go test ./...
```

Build local binaries:

```bash
go build -o ./bin/sealos-collector ./cmd/sealos-collector
go build -o ./bin/openstatus-sync ./cmd/openstatus-sync
```

Build a Linux container image:

```bash
docker buildx build --platform linux/amd64 -t sealos-collector:local .
```

## Docs

- [Architecture](./docs/architecture.md)
- [Runbook](./docs/runbook.md)
- [Information architecture](./docs/ia.md)
- [References](./docs/references.md)
- [Product notes](./PRODUCT.md)
- [Design notes](./DESIGN.md)
