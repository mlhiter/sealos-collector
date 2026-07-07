# AGENTS.md

## Project Rules

- Treat this repository as an independent collector project. Do not couple it to the Sealos monorepo or Sealos Desktop.
- The collector is read-only by default. Do not add Kubernetes write permissions unless the user explicitly asks for a mutating feature.
- The OpenStatus syncer may write OpenStatus page/component/status-report tables, but it must not write to the monitored Kubernetes cluster.
- Public snapshots must not include credentials, kubeconfigs, bearer tokens,
  URL userinfo, internal request headers, full internal queries, raw warning
  samples, or raw Secret data.
- Prefer small, explicit health checks over broad "monitor everything" behavior. A public status page should map platform evidence to user-facing service impact.
- Verify changes with `go test ./...` before handoff.
