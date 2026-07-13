# Roadmap

## Phase 1: Read-Only Snapshot MVP

- Load component/check config from YAML.
- Collect workload readiness, service endpoints, API readyz, HTTP checks,
  Prometheus queries, and Warning events.
- Emit a sanitized `summary.json`.
- Provide Kubernetes read-only RBAC examples.

## Phase 2: OpenStatus Adapter

- Sync snapshots into OpenStatus workspace/page/static page components by
  default.
- Add a Status Pipeline freshness component so stale `summary.json` data becomes
  visible and auto-resolves when fresh data returns.
- Create active status reports only for publishable non-operational components,
  while keeping symptom-only noise snapshot-only.
- Resolve collector-owned reports when components recover.
- Keep OpenStatus uptime/monitor components optional until real monitor runs
  exist.
- Run the adapter next to a self-hosted OpenStatus deployment.

## Phase 3: Publishing Hardening

- Explain metric threshold breaches with public-safe instant sample metadata.
- Publish aggregate ignored-warning categories without raw Warning samples.
- Prune last-known state for checks that are no longer in the current config.
- Add object-storage publishing targets such as S3/R2/OSS if another consumer
  needs snapshots.
- Add signature support for snapshots.
- Add stale snapshot metadata for downstream warning states.
- Keep manual approval for subscriber notifications.

## Phase 4: Product-Aware Sealos Checks

- Add first-class Sealos component presets with product-impact classifications.
- Add certificate and ingress expiry checks.
- Add product journey checks such as "create DevBox" or "list databases" where
  safe non-mutating probes exist.

## Phase 5: Multi-Cluster

- Support multiple collector agents reporting into one public status surface.
- Add region and cluster labels to snapshots.
- Aggregate regional status into global component status.
