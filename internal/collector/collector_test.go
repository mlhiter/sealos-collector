package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mlhiter/sealos-collector/internal/config"
	"github.com/mlhiter/sealos-collector/internal/status"
)

func TestCollectComponentWithoutChecksIsUnknown(t *testing.T) {
	c := &Collector{}

	component := c.collectComponent(context.Background(), config.ComponentConfig{
		ID:   "empty",
		Name: "Empty",
	})

	if component.Status != status.Unknown {
		t.Fatalf("Status = %s, want %s", component.Status, status.Unknown)
	}
	if component.Summary != "No checks configured" {
		t.Fatalf("Summary = %q, want no-checks summary", component.Summary)
	}
}

func TestCheckServiceEndpointsCountsReadyAddresses(t *testing.T) {
	ready := true
	notReady := false
	c := &Collector{
		kube: fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "desktop-abc",
				Namespace: "sealos",
				Labels: map[string]string{
					discoveryv1.LabelServiceName: "sealos-desktop",
				},
			},
			Endpoints: []discoveryv1.Endpoint{
				{
					Addresses:  []string{"10.0.0.1", "10.0.0.2"},
					Conditions: discoveryv1.EndpointConditions{Ready: &ready},
				},
				{
					Addresses:  []string{"10.0.0.3"},
					Conditions: discoveryv1.EndpointConditions{Ready: &notReady},
				},
			},
		}),
	}

	level, message, metadata := c.checkServiceEndpoints(context.Background(), config.CheckConfig{
		Namespace:   "sealos",
		ServiceName: "sealos-desktop",
		MinReady:    2,
	})

	if level != status.Operational {
		t.Fatalf("level = %s, want %s; message=%s", level, status.Operational, message)
	}
	if metadata["readyAddresses"] != "2" || metadata["addresses"] != "3" {
		t.Fatalf("metadata address counts = ready %s total %s, want ready 2 total 3", metadata["readyAddresses"], metadata["addresses"])
	}
	if metadata["endpointObjects"] != "2" || metadata["endpointSlices"] != "1" {
		t.Fatalf("metadata endpoint counts = objects %s slices %s, want objects 2 slices 1", metadata["endpointObjects"], metadata["endpointSlices"])
	}
}

func TestStatusExpected(t *testing.T) {
	if !statusExpected(http.StatusTemporaryRedirect, nil) {
		t.Fatal("default expected statuses should accept 3xx")
	}
	if statusExpected(http.StatusInternalServerError, nil) {
		t.Fatal("default expected statuses should reject 5xx")
	}
	if !statusExpected(http.StatusAccepted, []int{http.StatusAccepted}) {
		t.Fatal("explicit expected statuses should accept configured code")
	}
}

func TestSanitizedURLMetadataOmitsUserinfoAndQuery(t *testing.T) {
	got := sanitizedURLMetadata("https://user:pass@example.com:8443/health?token=secret")

	if got["scheme"] != "https" {
		t.Fatalf("scheme = %q, want https", got["scheme"])
	}
	if got["host"] != "example.com:8443" {
		t.Fatalf("host = %q, want host without userinfo", got["host"])
	}
	for key, value := range got {
		if strings.Contains(key, "token") ||
			strings.Contains(value, "user") ||
			strings.Contains(value, "pass") ||
			strings.Contains(value, "secret") {
			t.Fatalf("metadata leaked sensitive URL detail: %#v", got)
		}
	}
}

func TestCollectKeepsPublicChecksWhenDetailedChecksHidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := &Collector{
		cfg: &config.Config{
			Cluster: config.ClusterConfig{
				ID:   "dev-test",
				Name: "Dev Test",
				HTTP: config.HTTPConfig{Timeout: "1s"},
			},
			Publish: config.PublishConfig{IncludeCheckDetails: false},
			Components: []config.ComponentConfig{
				{
					ID:         "console",
					Name:       "Console",
					UserFacing: true,
					Checks: []config.CheckConfig{
						{
							ID:               "console-http",
							Name:             "Console external HTTP",
							Type:             "http",
							URL:              server.URL + "/?token=secret",
							ExpectedStatuses: []int{http.StatusOK},
						},
					},
				},
			},
		},
		state: &StateStore{
			state: persistedState{
				Version: "v1",
				Checks:  map[string]checkState{},
			},
		},
	}

	snapshot, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	component := snapshot.Components[0]
	if len(component.Checks) != 0 {
		t.Fatalf("Checks length = %d, want hidden detailed checks", len(component.Checks))
	}
	if len(component.PublicChecks) != 1 {
		t.Fatalf("PublicChecks length = %d, want 1", len(component.PublicChecks))
	}
	public := component.PublicChecks[0]
	if public.Impact != "" {
		t.Fatalf("Impact = %q, want empty legacy impact", public.Impact)
	}
	if public.Metadata["host"] == "" || public.Metadata["statusCode"] != "500" {
		t.Fatalf("public metadata = %#v, want host and statusCode", public.Metadata)
	}
	if public.ReasonCode != "http_unhealthy" {
		t.Fatalf("ReasonCode = %q, want http_unhealthy", public.ReasonCode)
	}
	if public.ImpactHint != "Console may be unavailable" {
		t.Fatalf("ImpactHint = %q, want Console unavailable hint", public.ImpactHint)
	}
	if public.SignalSummary != "Console external HTTP HTTP 500" {
		t.Fatalf("SignalSummary = %q, want HTTP status signal", public.SignalSummary)
	}
	if public.Confidence != "measurement" {
		t.Fatalf("Confidence = %q, want measurement", public.Confidence)
	}
	for key, value := range public.Metadata {
		if strings.Contains(key, "token") || strings.Contains(value, "secret") || strings.Contains(value, "token") {
			t.Fatalf("public metadata leaked sensitive query: %#v", public.Metadata)
		}
	}
}

func TestCollectPrunesStateKeysMissingFromConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := &Collector{
		cfg: &config.Config{
			Cluster: config.ClusterConfig{
				ID:   "dev-test",
				Name: "Dev Test",
				HTTP: config.HTTPConfig{Timeout: "1s"},
			},
			Publish: config.PublishConfig{IncludeCheckDetails: true},
			Components: []config.ComponentConfig{
				{
					ID:         "console",
					Name:       "Console",
					UserFacing: true,
					Checks: []config.CheckConfig{
						{
							ID:               "http",
							Name:             "Console external HTTP",
							Type:             "http",
							URL:              server.URL,
							ExpectedStatuses: []int{http.StatusOK},
							Timeout:          "1s",
						},
					},
				},
			},
		},
		state: &StateStore{
			state: persistedState{
				Version: "v1",
				Checks: map[string]checkState{
					"console/http": {
						LastStatus:   status.Degraded,
						LastMessage:  "previous signal",
						LastObserved: time.Now().Add(-time.Minute),
					},
					"templates/template-gogs-ready": {
						LastStatus:   status.Unknown,
						LastMessage:  "removed config",
						LastObserved: time.Now().Add(-time.Minute),
					},
				},
			},
		},
	}

	if _, err := c.Collect(context.Background()); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if _, ok := c.state.state.Checks["templates/template-gogs-ready"]; ok {
		t.Fatalf("stale state key was not pruned: %#v", c.state.state.Checks)
	}
	if got, ok := c.state.state.Checks["console/http"]; !ok || got.LastStatus != status.Operational {
		t.Fatalf("active state key = %#v, want operational current check", c.state.state.Checks["console/http"])
	}
}

func TestCollectIncludesNormalizedImpactAndStillHidesDetailedChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := &Collector{
		cfg: &config.Config{
			Cluster: config.ClusterConfig{
				ID:   "dev-test",
				Name: "Dev Test",
				HTTP: config.HTTPConfig{Timeout: "1s"},
			},
			Publish: config.PublishConfig{IncludeCheckDetails: false},
			Components: []config.ComponentConfig{
				{
					ID:         "console",
					Name:       "Console",
					UserFacing: true,
					Checks: []config.CheckConfig{
						{
							ID:               "console-http",
							Name:             "Console external HTTP",
							Type:             "http",
							Impact:           "serving-path",
							URL:              server.URL + "/health?token=secret",
							ExpectedStatuses: []int{http.StatusOK},
						},
					},
				},
			},
		},
		state: &StateStore{
			state: persistedState{
				Version: "v1",
				Checks:  map[string]checkState{},
			},
		},
	}

	snapshot, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	component := snapshot.Components[0]
	if len(component.Checks) != 0 {
		t.Fatalf("Checks length = %d, want hidden detailed checks", len(component.Checks))
	}
	public := component.PublicChecks[0]
	if public.Impact != config.CheckImpactServingPath {
		t.Fatalf("Impact = %q, want %q", public.Impact, config.CheckImpactServingPath)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{"token", "secret", "/health?", `"checks"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("public snapshot leaked %q: %s", forbidden, text)
		}
	}
}

func TestUnclassifiedCheckPreservesLegacyWorstStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := collectorWithHTTPCheck()

	component := c.collectComponent(context.Background(), config.ComponentConfig{
		ID:         "console",
		Name:       "Console",
		UserFacing: true,
		Checks: []config.CheckConfig{
			{
				ID:               "console-http",
				Name:             "Console external HTTP",
				Type:             "http",
				URL:              server.URL,
				ExpectedStatuses: []int{http.StatusOK},
			},
		},
	})

	if component.Status != status.Outage {
		t.Fatalf("Status = %s, want legacy raw outage", component.Status)
	}
}

func TestServingPathImpactKeepsOutage(t *testing.T) {
	result := status.CheckResult{Status: status.Outage, Impact: config.CheckImpactServingPath}

	if got := componentStatusForCheck(result); got != status.Outage {
		t.Fatalf("componentStatusForCheck() = %s, want outage", got)
	}
}

func TestComponentStatusForCheckMapsImpactToComponentStatus(t *testing.T) {
	tests := []struct {
		name      string
		rawStatus status.Level
		impact    string
		want      status.Level
	}{
		{name: "legacy outage", rawStatus: status.Outage, impact: "", want: status.Outage},
		{name: "legacy degraded", rawStatus: status.Degraded, impact: "", want: status.Degraded},
		{name: "legacy unknown", rawStatus: status.Unknown, impact: "", want: status.Unknown},
		{name: "serving path outage", rawStatus: status.Outage, impact: config.CheckImpactServingPath, want: status.Outage},
		{name: "control plane outage degrades", rawStatus: status.Outage, impact: config.CheckImpactControlPlane, want: status.Degraded},
		{name: "dependency outage degrades", rawStatus: status.Outage, impact: config.CheckImpactDependency, want: status.Degraded},
		{name: "symptom outage degrades", rawStatus: status.Outage, impact: config.CheckImpactSymptom, want: status.Degraded},
		{name: "informational outage ignored", rawStatus: status.Outage, impact: config.CheckImpactInformational, want: status.Operational},
		{name: "informational degraded ignored", rawStatus: status.Degraded, impact: config.CheckImpactInformational, want: status.Operational},
		{name: "informational unknown ignored", rawStatus: status.Unknown, impact: config.CheckImpactInformational, want: status.Operational},
		{name: "operational stays operational", rawStatus: status.Operational, impact: config.CheckImpactControlPlane, want: status.Operational},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := componentStatusForCheck(status.CheckResult{Status: tt.rawStatus, Impact: tt.impact})
			if got != tt.want {
				t.Fatalf("componentStatusForCheck() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestControlPlaneDependencyAndSymptomImpactsDegradeOutage(t *testing.T) {
	for _, impact := range []string{
		config.CheckImpactControlPlane,
		config.CheckImpactDependency,
		config.CheckImpactSymptom,
	} {
		t.Run(impact, func(t *testing.T) {
			result := status.CheckResult{Status: status.Outage, Impact: impact}
			if got := componentStatusForCheck(result); got != status.Degraded {
				t.Fatalf("componentStatusForCheck() = %s, want degraded", got)
			}
		})
	}
}

func TestInformationalImpactDoesNotAffectComponentStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := collectorWithHTTPCheck()

	component := c.collectComponent(context.Background(), config.ComponentConfig{
		ID:          "monitoring",
		Name:        "Monitoring",
		Description: "VictoriaMetrics query availability.",
		UserFacing:  false,
		Checks: []config.CheckConfig{
			{
				ID:               "dashboard-note",
				Name:             "Dashboard note",
				Type:             "http",
				Impact:           config.CheckImpactInformational,
				URL:              server.URL,
				ExpectedStatuses: []int{http.StatusOK},
			},
		},
	})

	if component.Status != status.Operational {
		t.Fatalf("Status = %s, want operational for informational failure", component.Status)
	}
	if component.Summary != "No user-facing impact detected" {
		t.Fatalf("Summary = %q, want no-impact summary", component.Summary)
	}
	if len(component.PublicChecks) != 1 {
		t.Fatalf("PublicChecks length = %d, want 1", len(component.PublicChecks))
	}
	public := component.PublicChecks[0]
	if public.Status != status.Outage || public.Impact != config.CheckImpactInformational {
		t.Fatalf("public status/impact = %s/%q, want outage/informational", public.Status, public.Impact)
	}
	if public.ImpactHint != "Monitoring has no confirmed user impact from this signal" {
		t.Fatalf("ImpactHint = %q, want no-impact hint", public.ImpactHint)
	}
}

func TestControlPlaneImpactDegradesComponentButKeepsRawCheckStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()

	c := collectorWithHTTPCheck()

	component := c.collectComponent(context.Background(), config.ComponentConfig{
		ID:          "devbox",
		Name:        "DevBox",
		Description: "Cloud development environment product surface.",
		UserFacing:  true,
		Checks: []config.CheckConfig{
			{
				ID:               "devbox-controller",
				Name:             "DevBox controller",
				Type:             "http",
				Impact:           config.CheckImpactControlPlane,
				URL:              server.URL,
				ExpectedStatuses: []int{http.StatusOK},
			},
		},
	})

	if component.Status != status.Degraded {
		t.Fatalf("Status = %s, want degraded", component.Status)
	}
	public := component.PublicChecks[0]
	if public.Status != status.Outage {
		t.Fatalf("Public check status = %s, want raw outage", public.Status)
	}
	if public.Impact != config.CheckImpactControlPlane {
		t.Fatalf("Public check impact = %q, want controlPlane", public.Impact)
	}
	if public.ImpactHint != "DevBox management operations may be degraded" {
		t.Fatalf("ImpactHint = %q, want management degraded hint", public.ImpactHint)
	}
}

func collectorWithHTTPCheck() *Collector {
	return &Collector{
		cfg: &config.Config{
			Cluster: config.ClusterConfig{
				HTTP: config.HTTPConfig{Timeout: "1s"},
			},
		},
		state: &StateStore{
			state: persistedState{
				Version: "v1",
				Checks:  map[string]checkState{},
			},
		},
	}
}

func TestPublicRecentWarningsExposeStructuredSemanticsWithoutSamples(t *testing.T) {
	public := publicCheckResults(config.ComponentConfig{
		ID:          "platform",
		Name:        "Platform Control Plane",
		Description: "Kubernetes API readiness and recent actionable warning events.",
		UserFacing:  false,
	}, status.Degraded, []status.CheckResult{
		{
			ID:      "warnings",
			Name:    "Recent actionable cluster warnings",
			Type:    "recentWarnings",
			Status:  status.Degraded,
			Message: "2 recent warning events",
			Metadata: map[string]string{
				"warnings":                         "2",
				"ignoredWarnings":                  "5",
				"ignoredTerminatingPodWarnings":    "1",
				"ignoredBenignConflictWarnings":    "1",
				"ignoredDeletedPodWarnings":        "1",
				"ignoredCompletedPodWarnings":      "1",
				"ignoredFailedPodWarnings":         "1",
				"ignoredPrivateImplementationNote": "should not be public",
				"since":                            "15m",
				"warningSample1":                   "objectstorage-frontend/Pod example-storage-pod-abc: Failed: Error: ImagePullBackOff",
			},
		},
	})

	if len(public) != 1 {
		t.Fatalf("PublicChecks length = %d, want 1", len(public))
	}
	got := public[0]
	if got.ReasonCode != "image_pull_failure" {
		t.Fatalf("ReasonCode = %q, want image_pull_failure", got.ReasonCode)
	}
	if got.SignalSummary != "Object Storage image pull failures; 2 warning events in 15m; 5 ignored as historical noise" {
		t.Fatalf("SignalSummary = %q, want product warning summary", got.SignalSummary)
	}
	if got.ImpactHint != "platform operations may be degraded; user-facing products can be affected if the signal persists" {
		t.Fatalf("ImpactHint = %q, want platform impact", got.ImpactHint)
	}
	if got.Confidence != "symptom" {
		t.Fatalf("Confidence = %q, want symptom", got.Confidence)
	}
	if _, ok := got.Metadata["warningSample1"]; ok {
		t.Fatalf("public metadata leaked warning sample: %#v", got.Metadata)
	}
	for _, key := range []string{
		"ignoredTerminatingPodWarnings",
		"ignoredBenignConflictWarnings",
		"ignoredDeletedPodWarnings",
		"ignoredCompletedPodWarnings",
		"ignoredFailedPodWarnings",
	} {
		if got.Metadata[key] != "1" {
			t.Fatalf("public metadata %s = %q, want safe ignored warning count", key, got.Metadata[key])
		}
	}
	if _, ok := got.Metadata["ignoredPrivateImplementationNote"]; ok {
		t.Fatalf("public metadata leaked non-whitelisted ignored warning key: %#v", got.Metadata)
	}
	for _, value := range []string{got.Message, got.SignalSummary, got.ImpactHint} {
		if strings.Contains(value, "example-storage-pod-abc") {
			t.Fatalf("public semantic field leaked pod sample: %#v", got)
		}
	}
}

func TestPublicCustomCheckSemanticsDoNotLeakRawMessage(t *testing.T) {
	public := publicCheckResults(config.ComponentConfig{
		ID:          "custom",
		Name:        "Custom Product",
		Description: "Custom product availability.",
		UserFacing:  true,
	}, status.Degraded, []status.CheckResult{
		{
			ID:      "custom-script",
			Name:    "Custom integration check",
			Type:    "customScript",
			Status:  status.Degraded,
			Message: "failed calling https://private.example.test/query?token=public-test-token",
		},
	})

	if len(public) != 1 {
		t.Fatalf("PublicChecks length = %d, want 1", len(public))
	}
	got := public[0]
	if got.ReasonCode != "check_failed" {
		t.Fatalf("ReasonCode = %q, want check_failed", got.ReasonCode)
	}
	if got.SignalSummary != "Custom integration check" {
		t.Fatalf("SignalSummary = %q, want check name only", got.SignalSummary)
	}
	if got.Message != "check failed" {
		t.Fatalf("Message = %q, want generic public message", got.Message)
	}
	for _, value := range []string{got.Message, got.SignalSummary, got.ImpactHint} {
		if strings.Contains(value, "private.example.test") || strings.Contains(value, "public-test-token") || strings.Contains(value, "https://") {
			t.Fatalf("public semantic field leaked raw custom detail: %#v", got)
		}
	}
}

func TestPublicWarningSampleRedactsURLsAndSecrets(t *testing.T) {
	got := publicWarningSample(corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "sealos"},
		Reason:     "Unhealthy",
		Message:    `Readiness probe failed: Get "http://192.0.2.80:3000/api/platform/getAppConfig?token=public-test-token": password=public-test-password`,
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Namespace: "sealos",
			Name:      "sealos-desktop-old",
		},
	})

	if !strings.Contains(got, "sealos/Pod sealos-desktop-old") || !strings.Contains(got, "Unhealthy") {
		t.Fatalf("sample = %q, want target and reason", got)
	}
	if strings.Contains(got, "192.0.2.80") || strings.Contains(got, "public-test-token") || strings.Contains(got, "public-test-password") {
		t.Fatalf("sample leaked raw endpoint or secret: %q", got)
	}
	if !strings.Contains(got, "<endpoint>") || !strings.Contains(got, "password=<redacted>") {
		t.Fatalf("sample = %q, want endpoint and password redaction", got)
	}
}

func TestCheckRecentWarningsCountsActivePodWarnings(t *testing.T) {
	now := metav1.Now()
	c := &Collector{
		kube: fake.NewSimpleClientset(
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-current",
					Namespace: "sealos",
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-current-warning",
					Namespace: "sealos",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "Unhealthy",
				Message:       "Readiness probe failed",
				LastTimestamp: now,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "sealos",
					Name:      "desktop-current",
				},
			},
		),
	}

	level, message, metadata := c.checkRecentWarnings(context.Background(), config.CheckConfig{
		Namespace:    "",
		Since:        "15m",
		WarningCount: 1,
	})

	if level != status.Degraded {
		t.Fatalf("level = %s, want %s; message=%s", level, status.Degraded, message)
	}
	if metadata["warnings"] != "1" || metadata["ignoredWarnings"] != "0" {
		t.Fatalf("metadata = %#v, want one actionable warning", metadata)
	}
}

func TestCheckRecentWarningsIgnoresInactivePodAndRetryNoise(t *testing.T) {
	now := metav1.Now()
	deletingTime := metav1.Now()
	c := &Collector{
		kube: fake.NewSimpleClientset(
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "desktop-terminating",
					Namespace:         "sealos",
					DeletionTimestamp: &deletingTime,
				},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-completed",
					Namespace: "sealos",
				},
				Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-failed",
					Namespace: "sealos",
				},
				Status: corev1.PodStatus{Phase: corev1.PodFailed},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-old-warning",
					Namespace: "sealos",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "Unhealthy",
				Message:       "Readiness probe failed",
				LastTimestamp: now,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "sealos",
					Name:      "desktop-old",
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-terminating-warning",
					Namespace: "sealos",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "Unhealthy",
				Message:       "Readiness probe failed",
				LastTimestamp: now,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "sealos",
					Name:      "desktop-terminating",
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-completed-warning",
					Namespace: "sealos",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "Unhealthy",
				Message:       "Readiness probe failed",
				LastTimestamp: now,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "sealos",
					Name:      "desktop-completed",
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "desktop-failed-warning",
					Namespace: "sealos",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "Unhealthy",
				Message:       "Readiness probe failed",
				LastTimestamp: now,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "sealos",
					Name:      "desktop-failed",
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "controller-retry-warning",
					Namespace: "kite-system",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "Warning",
				Message:       `Operation cannot be fulfilled on pods "kite-pg-postgresql-2": the object has been modified; please apply your changes to the latest version and try again`,
				LastTimestamp: now,
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Component",
					Namespace: "kite-system",
					Name:      "kite-pg-postgresql",
				},
			},
		),
	}

	level, message, metadata := c.checkRecentWarnings(context.Background(), config.CheckConfig{
		Namespace:    "",
		Since:        "15m",
		WarningCount: 1,
	})

	if level != status.Operational {
		t.Fatalf("level = %s, want %s; message=%s", level, status.Operational, message)
	}
	if metadata["warnings"] != "0" || metadata["ignoredWarnings"] != "5" {
		t.Fatalf("metadata = %#v, want five ignored warnings", metadata)
	}
	wantIgnored := map[string]string{
		"ignoredDeletedPodWarnings":     "1",
		"ignoredTerminatingPodWarnings": "1",
		"ignoredCompletedPodWarnings":   "1",
		"ignoredFailedPodWarnings":      "1",
		"ignoredBenignConflictWarnings": "1",
	}
	for key, want := range wantIgnored {
		if metadata[key] != want {
			t.Fatalf("metadata[%s] = %q, want %q in %#v", key, metadata[key], want, metadata)
		}
	}
	if got := warningSignalSummary(metadata); got != "recent Kubernetes warning events; 0 warning events in 15m0s; 5 ignored as historical noise" {
		t.Fatalf("warningSignalSummary() = %q, want ignored warning summary", got)
	}
}

func TestCheckPrometheusQueryExposesThresholdMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Fatalf("path = %q, want /api/v1/query", r.URL.Path)
		}
		if r.URL.Query().Get("query") == "" {
			t.Fatal("prometheus query parameter is required")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"result": [
					{"value": [1720000000, "1.105"]}
				]
			}
		}`))
	}))
	defer server.Close()

	warningAbove := 1.0
	criticalAbove := 3.0
	c := &Collector{
		cfg: &config.Config{
			Cluster: config.ClusterConfig{
				Prometheus: config.PrometheusConfig{Timeout: "1s"},
			},
		},
	}

	level, message, metadata := c.checkPrometheusQuery(context.Background(), config.CheckConfig{
		URL:           server.URL,
		Query:         `histogram_quantile(0.99, sum(rate(apiserver_request_duration_seconds_bucket{token="secret"}[5m])) by (le))`,
		WarningAbove:  &warningAbove,
		CriticalAbove: &criticalAbove,
		Timeout:       "1s",
	})

	if level != status.Degraded {
		t.Fatalf("level = %s, want %s; message=%s", level, status.Degraded, message)
	}
	if metadata["threshold"] != "1" || metadata["thresholdDirection"] != "above" || metadata["thresholdSeverity"] != "warning" || metadata["sampleType"] != "instant" {
		t.Fatalf("metadata = %#v, want threshold explanation", metadata)
	}

	public := publicCheckResults(config.ComponentConfig{
		ID:          "platform",
		Name:        "Platform Control Plane",
		Description: "Kubernetes API readiness and latency.",
	}, level, []status.CheckResult{
		{
			ID:       "latency",
			Name:     "Kubernetes API p99 latency",
			Type:     "prometheusQuery",
			Impact:   config.CheckImpactControlPlane,
			Status:   level,
			Message:  message,
			Metadata: metadata,
		},
	})

	if len(public) != 1 {
		t.Fatalf("PublicChecks length = %d, want 1", len(public))
	}
	got := public[0]
	if got.SignalSummary != "Kubernetes API p99 latency value 1.105 > warning threshold 1" {
		t.Fatalf("SignalSummary = %q, want threshold summary", got.SignalSummary)
	}
	if got.ReasonCode != "metric_threshold_breached" {
		t.Fatalf("ReasonCode = %q, want metric_threshold_breached", got.ReasonCode)
	}
	if got.Metadata["threshold"] != "1" || got.Metadata["thresholdDirection"] != "above" || got.Metadata["thresholdSeverity"] != "warning" || got.Metadata["sampleType"] != "instant" {
		t.Fatalf("public metadata = %#v, want safe threshold fields", got.Metadata)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal public check: %v", err)
	}
	for _, forbidden := range []string{"histogram_quantile", "token", "secret", "query"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public check leaked prometheus query detail %q: %s", forbidden, raw)
		}
	}
}

func TestDecodePrometheusValue(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{
			"status": "success",
			"data": {
				"result": [
					{"value": [1720000000, "42.5"]}
				]
			}
		}`)),
	}

	value, err := decodePrometheusValue(resp)
	if err != nil {
		t.Fatalf("decodePrometheusValue() error = %v", err)
	}
	if value != 42.5 {
		t.Fatalf("value = %v, want 42.5", value)
	}
}

func TestStateStoreCarriesForwardRecentKnownStatus(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	store := &StateStore{
		state: persistedState{
			Version: "v1",
			Checks: map[string]checkState{
				"console/http": {
					LastStatus:   status.Operational,
					LastMessage:  "http returned 200",
					LastObserved: now.Add(-time.Minute),
				},
			},
		},
	}

	got := store.StabilizeUnknown("console/http", status.CheckResult{
		ID:         "http",
		Status:     status.Unknown,
		Message:    "temporary read failed",
		ObservedAt: now,
	}, config.StatusPolicyConfig{UnknownGraceRuns: 2, StaleAfter: "10m"}, now)

	if got.Status != status.Operational {
		t.Fatalf("Status = %s, want carried operational", got.Status)
	}
	if got.Metadata["stabilized"] != "true" || got.Metadata["lastKnownStatus"] != string(status.Operational) {
		t.Fatalf("metadata = %#v, want stabilized last known status", got.Metadata)
	}
}

func TestStateStoreTurnsRepeatedUnknownIntoDegraded(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	store := &StateStore{
		state: persistedState{
			Version: "v1",
			Checks: map[string]checkState{
				"console/http": {
					LastStatus:    status.Operational,
					LastMessage:   "http returned 200",
					LastObserved:  now.Add(-time.Minute),
					UnknownStreak: 2,
				},
			},
		},
	}

	got := store.StabilizeUnknown("console/http", status.CheckResult{
		ID:         "http",
		Status:     status.Unknown,
		Message:    "temporary read failed",
		ObservedAt: now,
	}, config.StatusPolicyConfig{UnknownGraceRuns: 2, StaleAfter: "10m"}, now)

	if got.Status != status.Degraded {
		t.Fatalf("Status = %s, want degraded after grace", got.Status)
	}
	if got.Metadata["stabilized"] != "false" || got.Metadata["unknownStreak"] != "3" {
		t.Fatalf("metadata = %#v, want stale degraded signal", got.Metadata)
	}
}

func TestStateStorePrunesStaleKeys(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	store := &StateStore{
		state: persistedState{
			Version: "v1",
			Checks: map[string]checkState{
				"console/http": {
					LastStatus:   status.Operational,
					LastMessage:  "http returned 200",
					LastObserved: now,
				},
				"templates/template-gogs-ready": {
					LastStatus:   status.Degraded,
					LastMessage:  "removed config",
					LastObserved: now,
				},
			},
		},
	}

	removed := store.Prune(map[string]struct{}{
		"console/http": {},
	})

	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if !store.dirty {
		t.Fatal("store.dirty = false, want true after pruning")
	}
	if _, ok := store.state.Checks["templates/template-gogs-ready"]; ok {
		t.Fatalf("stale key was not pruned: %#v", store.state.Checks)
	}
	if _, ok := store.state.Checks["console/http"]; !ok {
		t.Fatalf("active key was pruned: %#v", store.state.Checks)
	}
}

func TestSafeNetworkErrorOmitsRawURL(t *testing.T) {
	err := &url.Error{
		Op:  "Get",
		URL: "https://token@example.com/path?secret=1",
		Err: errors.New("connection refused"),
	}

	got := safeNetworkError(err)
	if strings.Contains(got, "secret") || strings.Contains(got, "token") || strings.Contains(got, "example.com/path") {
		t.Fatalf("safeNetworkError() leaked raw URL details: %q", got)
	}
	if got != "connection refused" {
		t.Fatalf("safeNetworkError() = %q, want wrapped error body", got)
	}
}
