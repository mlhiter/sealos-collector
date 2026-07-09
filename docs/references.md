# References

## Local Project References

- `configs/sealos.example.yaml`: example Sealos component mapping.
- `deploy/rbac.yaml`: read-only Kubernetes permissions.
- `deploy/cronjob.yaml`: sample in-cluster execution.
- `cmd/openstatus-sync`: OpenStatus libSQL adapter.
- `internal/collector/state.go`: lightweight last-known check state and stale
  key pruning.
- `internal/status/status.go`: public snapshot schema, including freshness
  max-age metadata.

## External Concepts

- Kubernetes readiness, EndpointSlices, Events, and workload status.
- Prometheus-compatible instant query API.
- OpenStatus static page components and status reports as the default public
  status-page surface; OpenStatus monitor components are optional when real
  monitor runs are available.
- Snapshot freshness and generated Status Pipeline components for status-page
  self-observability.
- Public-safe signal semantics: metric threshold relationships and aggregate
  ignored Warning counters, not raw PromQL or Warning samples.
- Product health impact classifications: `servingPath`, `controlPlane`,
  `dependency`, `symptom`, and `informational`.

## Example Sealos Evidence

The example configs reflect a common Sealos-style cluster shape with:

- `sealos` namespace for desktop and monitor services.
- `applaunchpad-frontend` namespace for App Launchpad frontend.
- `dbprovider-frontend` namespace for Database frontend.
- `vm` namespace for VictoriaMetrics services.

`configs/host.example.yaml` classifies every check with product impact values.
Treat the namespaces and resource names as examples, not universal defaults.
