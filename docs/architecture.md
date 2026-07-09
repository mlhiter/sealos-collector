# Architecture

```text
Monitored Sealos Cluster
  Kubernetes API
  VictoriaMetrics / Prometheus
  HTTP health endpoints
  Kubernetes Events
          |
          v
  sealos-collector
          |
          v
  summary.json snapshot
          |
          v
  openstatus-sync
          |
          v
OpenStatus libSQL -> OpenStatus status-page UI
```

## Components

- CLI entrypoint: `cmd/sealos-collector`.
- OpenStatus adapter entrypoint: `cmd/openstatus-sync`.
- Config loader: `internal/config`.
- Check runner: `internal/collector`.
- OpenStatus syncer: `internal/openstatus`.
- Snapshot schema: `internal/status`.

## Data Flow

1. Load YAML config.
2. Build a Kubernetes client from explicit kubeconfig, `KUBECONFIG`, in-cluster
   config, or local `~/.kube/config`.
3. Run each configured component check. Components without checks are marked
   `unknown` instead of healthy.
4. Map each raw check status through its optional product `impact`, then
   aggregate the mapped values into component status. Unclassified checks keep
   legacy worst-status behavior.
5. Build `publicChecks` from a strict safe-metadata whitelist and attach
   structured `reasonCode`, `impactHint`, `signalSummary`, and `confidence`
   fields so public incident updates can summarize cause, impact, and signal
   without publishing raw internal check details.
6. Prune optional last-known check state against the current config so retired
   checks cannot keep affecting future stabilization decisions.
7. Aggregate component status into overall status.
8. Write JSON atomically to the configured output path.
9. `openstatus-sync` reads the JSON snapshot and upserts OpenStatus workspace,
   page, page components, component groups, and status reports. By default it
   uses static page components and keeps OpenStatus uptime monitors disabled.

## Failure Handling

Individual check failures become raw check statuses. A failed Prometheus query,
for example, should not crash the collector. The optional check `impact` field
then translates raw status into user-facing component impact: `servingPath`
keeps raw outages as outages; `controlPlane`, `dependency`, and `symptom`
outages become degraded component status; `informational` signals do not change
component status. Transient `unknown` checks can use recent last-known state
according to `statusPolicy`; repeated or stale unknown signals become
`degraded` instead of producing endless public unknown noise.
The `recentWarnings` check treats Kubernetes Warning events as current evidence,
not as a raw event-history counter: warnings for deleted, terminating, or
completed Pods and common controller retry conflicts are ignored and summarized
only as safe ignored-category counts. Prometheus-compatible checks use instant
query samples; when a threshold fires, public metadata records the sample value,
threshold, direction, severity, and `sampleType=instant` without storing the raw
query text.

Config, output write, or OpenStatus sync failures are process failures for the
corresponding command.

## Publishing Boundary

The Kubernetes CronJob example writes the snapshot to stdout only. This keeps
cluster collection read-only and avoids pretending an in-cluster `emptyDir` is a
public status source.

The host deployment uses OpenStatus as the only public UI. `summary.json` is a
backend exchange file between `sealos-collector` and `openstatus-sync`, not a
user-facing page.

## OpenStatus Mapping

- Snapshot component -> OpenStatus static page component when
  `--show-uptime=false`.
- Snapshot component -> OpenStatus monitor page component only when
  `--show-uptime=true` and real OpenStatus monitor data is expected.
- Snapshot group -> OpenStatus page component group.
- `operational` -> no active collector-owned status report.
- `degraded` -> active status report with `degraded_performance` impact.
- `outage` -> active status report with `major_outage` impact.
- `unknown` -> active status report with `degraded_performance` impact, because
  the collector lacks evidence of a full outage.

When a component returns to `operational`, the syncer resolves the active
collector-owned report and adds a resolved update. Non-operational updates are
Incident Digests rendered from collector-provided `publicChecks` semantics: a
headline cause, an impact sentence, and at most two safe signals. The syncer is
a display adapter; warning-event classification and namespace-to-product mapping
belong to the collector health model. Raw pod samples, image URLs, internal
Prometheus query details, and ignored-warning samples stay out of public status
report messages; metric digests use the collector-provided threshold
relationship when available.
Resolved updates are intentionally one line so public incident history stays
dense. When a component is removed from collector scope, the syncer resolves its
stale active collector-owned report so retired components do not stay on the
public banner.
