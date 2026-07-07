# Product

Sealos Collector is a public-status-page evidence adapter for Sealos-like
cloud platforms.

## Target User

- Platform operators who need to publish credible user-facing health status.
- Status-page systems that need platform-specific evidence beyond HTTP uptime.
- On-call engineers who need a compact health snapshot before writing incident
  updates.

## Problem

Public status pages often know whether a URL is reachable, but they do not know
why a cloud platform is degraded. Kubernetes, metrics, ingress, certificate,
and product-service evidence lives inside the platform and needs to be translated
into a user-facing component status.

## Product Promise

The collector turns internal read-only platform signals into OpenStatus-ready
component state:

- Overall platform status.
- User-facing component status.
- Product-impact classification that keeps raw check evidence while mapping
  serving paths, control planes, dependencies, symptoms, and informational
  signals to the right public user impact.
- Optional check-level evidence for private consumers.
- Clear last-updated time for stale snapshot detection.
- OpenStatus page components and status report lifecycle for the public page.

## Non-Goals

- Hosting a public status page.
- Replacing OpenStatus, Better Stack, Statuspage, Grafana, or alerting systems.
- Performing remediation or Kubernetes writes.
- Storing long-term incident history.
- Exposing raw cluster internals directly to the public.
