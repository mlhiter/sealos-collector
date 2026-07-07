package openstatus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mlhiter/sealos-collector/internal/status"
)

func TestImpactForLevel(t *testing.T) {
	tests := map[status.Level]string{
		status.Operational: "operational",
		status.Degraded:    "degraded_performance",
		status.Outage:      "major_outage",
		status.Unknown:     "degraded_performance",
	}

	for level, want := range tests {
		if got := impactForLevel(level); got != want {
			t.Fatalf("impactForLevel(%q) = %q, want %q", level, got, want)
		}
	}
}

func TestReportStatusForLevel(t *testing.T) {
	if got := reportStatusForLevel(status.Unknown); got != "investigating" {
		t.Fatalf("reportStatusForLevel(unknown) = %q, want investigating", got)
	}
	if got := reportStatusForLevel(status.Outage); got != "identified" {
		t.Fatalf("reportStatusForLevel(outage) = %q, want identified", got)
	}
}

func TestSQLQuoteEscapesSingleQuotes(t *testing.T) {
	got := sqlQuote("Sealos user's Console")
	want := "'Sealos user''s Console'"
	if got != want {
		t.Fatalf("sqlQuote() = %q, want %q", got, want)
	}
}

func TestFilterComponentsIncludesInternalWhenRequested(t *testing.T) {
	components := []status.Component{
		{Name: "Console", UserFacing: true},
		{Name: "Control Plane", UserFacing: false},
	}

	if got := len(filterComponents(components, false)); got != 1 {
		t.Fatalf("filterComponents(false) returned %d components, want 1", got)
	}
	if got := len(filterComponents(components, true)); got != 2 {
		t.Fatalf("filterComponents(true) returned %d components, want 2", got)
	}
}

func TestPageConfigurationCanHideUptime(t *testing.T) {
	got, err := pageConfiguration(false)
	if err != nil {
		t.Fatalf("pageConfiguration() error = %v", err)
	}
	if !strings.Contains(got, `"uptime":false`) {
		t.Fatalf("configuration = %s, want uptime false", got)
	}
}

func TestReportUpdateMessageUsesCompactDiagnostics(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:        "Console",
		Description: "Login and desktop entry points.",
		Status:      status.Outage,
		Summary:     "One or more checks are failing",
		PublicChecks: []status.PublicCheckResult{
			{
				Name:          "Desktop deployment",
				Type:          "workload",
				Status:        status.Outage,
				Message:       "0/1 replicas ready",
				ReasonCode:    "workload_not_ready",
				ImpactHint:    "Login and desktop entry points may be unavailable",
				SignalSummary: "Desktop deployment 0/1 ready",
				Confidence:    "measurement",
				Metadata: map[string]string{
					"namespace": "sealos",
					"kind":      "Deployment",
					"resource":  "sealos-desktop",
					"ready":     "0",
					"desired":   "1",
					"minReady":  "1",
				},
			},
			{
				Name:          "Console external HTTP",
				Type:          "http",
				Status:        status.Outage,
				Message:       "http request failed: context deadline exceeded",
				ReasonCode:    "http_unreachable",
				ImpactHint:    "Login and desktop entry points may be unavailable",
				SignalSummary: "Console external HTTP unreachable",
				Confidence:    "measurement",
				Metadata: map[string]string{
					"scheme": "https",
					"host":   "console.example.com",
				},
			},
		},
	}, false)

	for _, want := range []string{
		"Console outage: Desktop deployment is not ready (0/1 ready).",
		"Impact: Login and desktop entry points may be unavailable.",
		"Signal: Desktop deployment 0/1 ready; Console external HTTP unreachable.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	for _, notWant := range []string{"Failing checks:", "resource=", "target=https://", "console.example.com"} {
		if strings.Contains(message, notWant) {
			t.Fatalf("message contains verbose marker %q:\n%s", notWant, message)
		}
	}
}

func TestReportUpdateMessageUsesCompactWarningEvidence(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:        "Platform Control Plane",
		Description: "Kubernetes API readiness and recent actionable warning events.",
		Status:      status.Degraded,
		Summary:     "One or more checks are degraded",
		PublicChecks: []status.PublicCheckResult{
			{
				Name:          "Recent actionable cluster warnings",
				Type:          "recentWarnings",
				Status:        status.Degraded,
				Message:       "2 recent warning events",
				ReasonCode:    "image_pull_failure",
				ImpactHint:    "platform operations may be degraded; user-facing products can be affected if the signal persists",
				SignalSummary: "Object Storage image pull failures; 2 warning events in 15m",
				Confidence:    "symptom",
				Metadata: map[string]string{
					"warnings":        "2",
					"ignoredWarnings": "1",
					"since":           "15m",
				},
			},
		},
	}, false)

	for _, want := range []string{
		"Platform Control Plane degraded: Object Storage image pull failures detected.",
		"Impact: platform operations may be degraded; user-facing products can be affected if the signal persists.",
		"Signal: Object Storage image pull failures; 2 warning events in 15m.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	for _, notWant := range []string{"Failing checks:", "Evidence:", "ignored=1", "example-storage-pod-abc"} {
		if strings.Contains(message, notWant) {
			t.Fatalf("message contains verbose/noisy marker %q:\n%s", notWant, message)
		}
	}
}

func TestReportUpdateMessageUsesDigestForMetricSignal(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:        "Platform Control Plane",
		Description: "Kubernetes API readiness and recent actionable warning events.",
		Status:      status.Degraded,
		Summary:     "One or more checks are degraded",
		PublicChecks: []status.PublicCheckResult{
			{
				Name:          "Kubernetes API p99 latency",
				Type:          "prometheusQuery",
				Status:        status.Degraded,
				Message:       "metric value 0.338",
				ReasonCode:    "metric_threshold_breached",
				ImpactHint:    "platform operations may be degraded; user-facing products can be affected if the signal persists",
				SignalSummary: "Kubernetes API p99 latency value 0.338",
				Confidence:    "measurement",
				Metadata: map[string]string{
					"host":  "prometheus.example.internal:8427",
					"value": "0.338",
				},
			},
		},
	}, false)

	for _, want := range []string{
		"Platform Control Plane degraded: Kubernetes API p99 latency breached its health threshold value 0.338.",
		"Signal: Kubernetes API p99 latency value 0.338.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "prometheus.example.internal") {
		t.Fatalf("message leaked prometheus host:\n%s", message)
	}
}

func TestReportUpdateMessageFallsBackConservativelyForLegacyWarningSamples(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:        "Platform Control Plane",
		Description: "Kubernetes API readiness and recent actionable warning events.",
		Status:      status.Degraded,
		Summary:     "One or more checks are degraded",
		PublicChecks: []status.PublicCheckResult{
			{
				Name:    "Recent actionable cluster warnings",
				Type:    "recentWarnings",
				Status:  status.Degraded,
				Message: "2 recent warning events",
				Metadata: map[string]string{
					"warnings":       "2",
					"since":          "15m",
					"warningSample1": "objectstorage-frontend/Pod example-storage-pod-abc: Failed: Error: ImagePullBackOff",
				},
			},
		},
	}, false)

	for _, want := range []string{
		"Platform Control Plane degraded: recent Kubernetes warning events crossed the threshold (2 warning events in 15m).",
		"Signal: 2 warning events in 15m.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	for _, notWant := range []string{"Object Storage", "image pull failures", "example-storage-pod-abc"} {
		if strings.Contains(message, notWant) {
			t.Fatalf("message used legacy raw warning sample %q:\n%s", notWant, message)
		}
	}
}

func TestReportUpdateMessageDoesNotLeakUnknownRawCheckMessage(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:        "Custom Product",
		Description: "Custom product availability.",
		Status:      status.Degraded,
		Summary:     "One or more checks are degraded",
		PublicChecks: []status.PublicCheckResult{
			{
				Name:    "Custom integration check",
				Type:    "customScript",
				Status:  status.Degraded,
				Message: "failed calling https://private.example.test/query?token=public-test-token",
			},
		},
	}, false)

	for _, want := range []string{
		"Custom Product degraded: Custom integration check check failed.",
		"Impact: Custom product availability may be degraded.",
		"Signal: Custom integration check.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	for _, notWant := range []string{"private.example.test", "public-test-token", "https://"} {
		if strings.Contains(message, notWant) {
			t.Fatalf("message leaked raw custom check detail %q:\n%s", notWant, message)
		}
	}
}

func TestReportUpdateMessageUsesCompactRecovery(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:    "Platform Control Plane",
		Status:  status.Operational,
		Summary: "All checks passed",
		PublicChecks: []status.PublicCheckResult{
			{Name: "Kubernetes API readyz", Status: status.Operational, Message: "readyz passed"},
			{Name: "Recent actionable cluster warnings", Status: status.Operational, Message: "no recent warning events above threshold"},
		},
	}, true)

	if message != "Platform Control Plane recovered: 2 checks operational." {
		t.Fatalf("message = %q, want compact recovery", message)
	}
}

func TestReportUpdateMessageLegacyRecoveryDoesNotLeakRawDetailedCheckMessage(t *testing.T) {
	message := reportUpdateMessage(status.Component{
		Name:    "Custom Product",
		Status:  status.Operational,
		Summary: "All checks passed",
		Checks: []status.CheckResult{
			{
				Name:    "Custom integration check",
				Type:    "customScript",
				Status:  status.Operational,
				Message: "recovered after calling https://private.example.test/query?token=public-test-token",
			},
		},
	}, true)

	if message != "Custom Product recovered: check passed." {
		t.Fatalf("message = %q, want generic legacy recovery", message)
	}
	for _, notWant := range []string{"private.example.test", "public-test-token", "https://"} {
		if strings.Contains(message, notWant) {
			t.Fatalf("message leaked raw detailed check detail %q:\n%s", notWant, message)
		}
	}
}

func TestSyncWithoutUptimeUsesStaticComponents(t *testing.T) {
	var statements []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request pipelineRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Requests) == 0 || request.Requests[0].Stmt == nil {
			t.Errorf("request had no executable statement")
			http.Error(w, "missing statement", http.StatusBadRequest)
			return
		}

		sql := request.Requests[0].Stmt.SQL
		statements = append(statements, sql)
		response := pipelineResponse{
			Results: []pipelineResult{
				{
					Type: "ok",
					Response: pipelineResponseItem{
						Type:   "execute",
						Result: fakeSyncResult(sql),
					},
				},
			},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	syncer, err := NewSyncer(Options{
		DatabaseURL:   server.URL,
		WorkspaceSlug: "sealos",
		WorkspaceName: "Sealos",
		PageSlug:      "sealos-status",
		PageTitle:     "Sealos Status",
		ShowUptime:    false,
	})
	if err != nil {
		t.Fatalf("NewSyncer() error = %v", err)
	}

	result, err := syncer.Sync(context.Background(), &status.Snapshot{
		Version:     "v1",
		GeneratedAt: time.Unix(100, 0).UTC(),
		Cluster: status.Cluster{
			ID:   "example-cluster",
			Name: "Example Sealos Cluster",
		},
		Components: []status.Component{
			{
				ID:          "app-launchpad",
				Name:        "App Launchpad",
				Description: "Application deployment product surface.",
				UserFacing:  true,
				Status:      status.Operational,
				Summary:     "All checks passed",
			},
		},
	})
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Components != 1 {
		t.Fatalf("Components = %d, want 1", result.Components)
	}

	joined := strings.Join(statements, "\n")
	if strings.Contains(joined, "INSERT INTO monitor") {
		t.Fatalf("sync inserted monitor while uptime was hidden:\n%s", joined)
	}
	if !strings.Contains(joined, "m.name = 'sealos-collector:app-launchpad'") {
		t.Fatalf("sync did not look up existing collector monitor component:\n%s", joined)
	}
	if !strings.Contains(joined, "type = 'static', monitor_id = NULL") {
		t.Fatalf("sync did not convert page component to static:\n%s", joined)
	}
	if !strings.Contains(joined, "UPDATE monitor SET active = 0, public = 0") {
		t.Fatalf("sync did not disable collector monitor rows:\n%s", joined)
	}
}

func fakeSyncResult(sql string) *ResultSet {
	switch {
	case strings.Contains(sql, "SELECT id FROM workspace"):
		return resultWithInt64(1)
	case strings.Contains(sql, "SELECT id FROM page WHERE"):
		return resultWithInt64(2)
	case strings.Contains(sql, "m.name = 'sealos-collector:app-launchpad'"):
		return resultWithInt64(3)
	case strings.Contains(sql, "SELECT sr.id FROM status_report"):
		return &ResultSet{}
	case strings.Contains(sql, "pc.id NOT IN"):
		return &ResultSet{}
	default:
		return &ResultSet{}
	}
}

func resultWithInt64(value int64) *ResultSet {
	return &ResultSet{
		Rows: [][]Cell{
			{{Type: "integer", Value: strconv.FormatInt(value, 10)}},
		},
	}
}
