package collector

import (
	"context"
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
				"warnings":        "2",
				"ignoredWarnings": "1",
				"since":           "15m",
				"warningSample1":  "objectstorage-frontend/Pod example-storage-pod-abc: Failed: Error: ImagePullBackOff",
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
	if got.SignalSummary != "Object Storage image pull failures; 2 warning events in 15m" {
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
	c := &Collector{
		kube: fake.NewSimpleClientset(
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
	if metadata["warnings"] != "0" || metadata["ignoredWarnings"] != "2" {
		t.Fatalf("metadata = %#v, want two ignored warnings", metadata)
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
