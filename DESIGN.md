# Design

## Principles

- Public health is about user promises, not internal object names.
- Internal evidence should be gathered read-only and published as a sanitized
  snapshot.
- Public health also depends on freshness: an old green snapshot is not a valid
  claim that the platform is currently healthy.
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

Checks keep their raw status, then an optional check `impact` maps that signal
to the component status. If `impact` is omitted, component status remains the
worst raw check status for backward compatibility. This lets a direct serving
path failure become an `outage`, while a controller, dependency, or warning
symptom can make the product `degraded` without claiming that the direct user
entry point is unavailable. Overall status is the worst status among
components.

A component with no checks is `unknown`, because the collector has no evidence
that the user promise is healthy.

## Collector Boundaries

The collector can read:

- Kubernetes workloads and EndpointSlices.
- Kubernetes API readiness.
- Actionable Kubernetes Warning events. Historical warnings for deleted,
  terminating, or completed Pods and common controller retry conflicts do not
  degrade the public page by themselves. The public surface may show aggregate
  ignored-warning counters, but not raw event samples or object names.
- HTTP endpoints from the collector runtime.
- Prometheus-compatible query APIs. Public metric evidence is the instant
  sample and threshold relationship, not the raw PromQL expression.

The collector can classify read-only checks by product impact:

- `servingPath`: user entry point or data path; outage means the component may
  be unavailable.
- `controlPlane`: management or reconciliation path; outage degrades the
  component.
- `dependency`: supporting dependency; outage degrades the component.
- `symptom`: derived warning or metric signal; outage degrades the component.
- `informational`: published evidence that does not change component status.

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
raw check status, check impact, public-safe messages, safe metadata, and
structured incident semantics. The collector emits `reasonCode`, `impactHint`,
`signalSummary`, and `confidence` for non-operational checks so adapters do not
need to parse raw logs or Kubernetes event text. Public updates must not expose
credentials, kubeconfigs, bearer tokens, raw request headers, Secret data, full
internal queries, or raw warning samples.

The safe metadata whitelist is intentionally specific. `prometheusQuery`
publishes the sample value, threshold, threshold direction, threshold severity,
and `sampleType=instant`. `recentWarnings` publishes actionable and ignored
warning counts plus safe ignored-category counters such as deleted,
terminating, completed, failed, and benign retry-conflict warnings. A single
transient metric breach should be treated as an instant sample that needs the
next collection pass to confirm persistence.

## State Hygiene

The optional collector state file exists only to stabilize transient `unknown`
checks. State keys use `<componentID>/<checkID>` and are pruned after each
collection pass against the current config. Removing or renaming a check should
therefore remove its old last-known status instead of letting retired checks
affect future snapshots.

## Freshness Model

Every collector-run snapshot may include a `freshness` contract with the
expected collection interval and max tolerated snapshot age in seconds. The
collector derives the default max age from runtime settings: three collection
intervals for repeated collection, or five minutes for one-shot snapshots.

Freshness is not a Kubernetes health check. It is a status-page pipeline health
signal owned by `openstatus-sync`. The syncer adds one public `Status Pipeline`
component and marks it `degraded` when `generatedAt` is older than the max age.
That degraded state says the public page may be stale; it must not be confused
with a product outage in the monitored cluster.

## OpenStatus Integration

OpenStatus is the public communication surface. The backend adapter maps
snapshot components to OpenStatus static page components by default and maps
non-operational component states to active status reports. It renders
collector-provided incident semantics as dense Incident Digest updates instead
of classifying warning samples itself. It also renders the generated `Status
Pipeline` freshness component so stale status data becomes visible and resolves
when a fresh snapshot arrives. Recovery resolves the corresponding
collector-owned report. OpenStatus monitor components are an optional mode for a
later full uptime setup, not the default public display path.
