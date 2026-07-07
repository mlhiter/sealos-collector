package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
components:
  - id: console
    name: Console
    checks:
      - type: kubernetesReadyz
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Cluster.ID != "default" || cfg.Cluster.Name != "default" {
		t.Fatalf("cluster defaults = %q/%q, want default/default", cfg.Cluster.ID, cfg.Cluster.Name)
	}
	component := cfg.Components[0]
	if component.Group != "Other" {
		t.Fatalf("component group = %q, want Other", component.Group)
	}
	check := component.Checks[0]
	if check.ID != "console-check-1" || check.Name != "console-check-1" {
		t.Fatalf("check id/name = %q/%q, want generated id/name", check.ID, check.Name)
	}
	if check.Impact != "" {
		t.Fatalf("check impact = %q, want empty default", check.Impact)
	}
	if check.Timeout != "10s" || check.Since != "15m" || check.CriticalCount != 20 {
		t.Fatalf("check defaults = timeout %q since %q critical %d", check.Timeout, check.Since, check.CriticalCount)
	}
	if cfg.StatusPolicy.UnknownGraceRuns != 2 || cfg.StatusPolicy.StaleAfter != "10m" {
		t.Fatalf("status policy defaults = grace %d stale %q, want 2/10m", cfg.StatusPolicy.UnknownGraceRuns, cfg.StatusPolicy.StaleAfter)
	}
}

func TestLoadRejectsEmptyComponents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("components: []\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func TestLoadRejectsUnsupportedCheckImpact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
components:
  - id: console
    name: Console
    checks:
      - type: kubernetesReadyz
        impact: maybeCritical
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want unsupported impact error")
	}
	if !strings.Contains(err.Error(), `component "console" check "console-check-1" impact "maybeCritical" is not supported`) {
		t.Fatalf("Load() error = %q, want impact context", err)
	}
}

func TestLoadNormalizesCheckImpact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
components:
  - id: console
    name: Console
    checks:
      - id: serving
        type: http
        impact: serving-path
      - id: control
        type: http
        impact: CONTROL_PLANE
      - id: dependency
        type: http
        impact: dependency
      - id: symptom
        type: http
        impact: Symptom
      - id: info
        type: http
        impact: informational
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	checks := cfg.Components[0].Checks
	got := []string{}
	for _, check := range checks {
		got = append(got, check.Impact)
	}
	want := []string{
		CheckImpactServingPath,
		CheckImpactControlPlane,
		CheckImpactDependency,
		CheckImpactSymptom,
		CheckImpactInformational,
	}
	if len(got) != len(want) {
		t.Fatalf("impact count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("impact[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeCheckImpactAcceptsAliases(t *testing.T) {
	tests := map[string]string{
		"servingPath":   CheckImpactServingPath,
		"serving-path":  CheckImpactServingPath,
		"serving_path":  CheckImpactServingPath,
		"controlPlane":  CheckImpactControlPlane,
		"control plane": CheckImpactControlPlane,
		"dependency":    CheckImpactDependency,
		"symptom":       CheckImpactSymptom,
		"informational": CheckImpactInformational,
		"":              "",
		"unknown":       "",
	}

	for input, want := range tests {
		if got := NormalizeCheckImpact(input); got != want {
			t.Fatalf("NormalizeCheckImpact(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExampleConfigsLoad(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "configs", "sealos.example.yaml"),
		filepath.Join("..", "..", "configs", "host.example.yaml"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := Load(path); err != nil {
				t.Fatalf("Load(%s) error = %v", path, err)
			}
		})
	}
}

func TestHostExampleClassifiesAllChecks(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "host.example.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s) error = %v", path, err)
	}

	for _, component := range cfg.Components {
		for _, check := range component.Checks {
			if check.Impact == "" {
				t.Fatalf("component %q check %q has empty impact", component.ID, check.ID)
			}
		}
	}
}

func TestDurationOr(t *testing.T) {
	fallback := 30 * time.Second

	if got := DurationOr("5s", fallback); got != 5*time.Second {
		t.Fatalf("DurationOr(valid) = %s, want 5s", got)
	}
	if got := DurationOr("bad", fallback); got != fallback {
		t.Fatalf("DurationOr(invalid) = %s, want fallback %s", got, fallback)
	}
}
