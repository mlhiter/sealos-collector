# Design

## Principles

- Public health is about user promises, not internal object names.
- Internal evidence should be gathered read-only and published as a sanitized
  snapshot.
- The public status page must remain outside the monitored cluster and be served
  by OpenStatus, not a collector-owned frontend.
- Automatic checks provide facts and bounded health semantics; human incident
  updates provide operator judgement and narrative.

## Health Model

Statuses are intentionally small:

- `operational`: all configured checks pass.
- `unknown`: evidence could not be evaluated.
- `degraded`: the component is partially impaired.
- `outage`: the component has no viable serving path.

Component status is the worst status among its checks. Overall status is the
worst status among components.

A component with no checks is `unknown`, because the collector has no evidence
that the user promise is healthy.

## Collector Boundaries

The collector can read:

- Kubernetes workloads and EndpointSlices.
- Kubernetes API readiness.
- Actionable Kubernetes Warning events. Historical warnings for deleted,
  terminating, or completed Pods and common controller retry conflicts do not
  degrade the public page by themselves.
- HTTP endpoints from the collector runtime.
- Prometheus-compatible query APIs.

The collector must not:

- Read Secrets.
- Write Kubernetes resources.
- Publish kubeconfigs, tokens, or raw credentials.
- Invent long-form incident timelines or final operator narrative on its own.
- Serve a custom public status UI.

## Public Snapshot Boundary

`publish.includeCheckDetails` controls whether raw check results appear in the
snapshot. Public pages should normally set it to `false`; private dashboards may
set it to `true`.

Public snapshots still include `publicChecks`: a sanitized, whitelisted view of
check status, public-safe messages, safe metadata, and structured incident
semantics. The collector emits `reasonCode`, `impactHint`, `signalSummary`, and
`confidence` for non-operational checks so adapters do not need to parse raw
logs or Kubernetes event text. Public updates must not expose credentials,
kubeconfigs, bearer tokens, raw request headers, Secret data, full internal
queries, or raw warning samples.

## OpenStatus Integration

OpenStatus is the public communication surface. The backend adapter maps
snapshot components to OpenStatus static page components by default and maps
non-operational component states to active status reports. It renders
collector-provided incident semantics as dense Incident Digest updates instead
of classifying warning samples itself. Recovery resolves the corresponding
collector-owned report. OpenStatus monitor components are an optional mode for
a later full uptime setup, not the default public display path.
