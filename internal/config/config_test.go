package config

import (
	"os"
	"path/filepath"
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

func TestDurationOr(t *testing.T) {
	fallback := 30 * time.Second

	if got := DurationOr("5s", fallback); got != 5*time.Second {
		t.Fatalf("DurationOr(valid) = %s, want 5s", got)
	}
	if got := DurationOr("bad", fallback); got != fallback {
		t.Fatalf("DurationOr(invalid) = %s, want fallback %s", got, fallback)
	}
}
