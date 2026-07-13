# Information Architecture

The snapshot is organized by user-facing platform capabilities.

Recommended groups:

- Access: console, login, public entry points.
- Products: App Launchpad, Cost Center, CronJob, Database, DevBox, Kite,
  Object Storage, App Store, Terminal.
- Infrastructure: Kubernetes, monitoring, logging, ingress, certificates,
  storage.
- Business Systems: billing and account services when they are exposed as
  user-facing platform promises.

OpenStatus public pages should show:

- Overall status.
- Status Pipeline freshness when the public page is showing current data or
  stale data.
- User-facing and platform-internal components grouped by capability when the
  goal is whole-platform health.
- Open collector-owned reports for publishable degraded, outage, or unknown
  component states.
- Human incident or maintenance notes when operators need richer wording.

Public pages should avoid:

- Internal namespace names unless intentionally exposed.
- Raw pod names.
- Raw Kubernetes Warning messages or samples.
- Raw PromQL expressions.
- Stack traces.
- Credential-like URLs or headers.
