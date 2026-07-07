# References

## Local Project References

- `configs/sealos.example.yaml`: example Sealos component mapping.
- `deploy/rbac.yaml`: read-only Kubernetes permissions.
- `deploy/cronjob.yaml`: sample in-cluster execution.
- `cmd/openstatus-sync`: OpenStatus libSQL adapter.

## External Concepts

- Kubernetes readiness, EndpointSlices, Events, and workload status.
- Prometheus-compatible instant query API.
- OpenStatus static page components and status reports as the default public
  status-page surface; OpenStatus monitor components are optional when real
  monitor runs are available.

## Example Sealos Evidence

The example configs reflect a common Sealos-style cluster shape with:

- `sealos` namespace for desktop and monitor services.
- `applaunchpad-frontend` namespace for App Launchpad frontend.
- `dbprovider-frontend` namespace for Database frontend.
- `vm` namespace for VictoriaMetrics services.

Treat these as examples, not universal defaults.
