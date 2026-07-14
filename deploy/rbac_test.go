package deploy

import (
	"io"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

type rbacManifest struct {
	Kind  string     `yaml:"kind"`
	Rules []rbacRule `yaml:"rules"`
}

type rbacRule struct {
	APIGroups []string `yaml:"apiGroups"`
	Resources []string `yaml:"resources"`
	Verbs     []string `yaml:"verbs"`
}

func TestCollectorClusterRole(t *testing.T) {
	file, err := os.Open("rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	var role rbacManifest
	for {
		var manifest rbacManifest
		if err := decoder.Decode(&manifest); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if manifest.Kind == "ClusterRole" {
			role = manifest
			break
		}
	}
	if role.Kind == "" {
		t.Fatal("ClusterRole not found in rbac.yaml")
	}

	allowedVerbs := map[string]bool{"get": true, "list": true, "watch": true}
	hasPodGet := false
	for _, rule := range role.Rules {
		for _, verb := range rule.Verbs {
			if !allowedVerbs[verb] {
				t.Fatalf("ClusterRole contains mutating verb %q", verb)
			}
		}
		if contains(rule.APIGroups, "") && contains(rule.Resources, "pods") && contains(rule.Verbs, "get") {
			hasPodGet = true
		}
	}
	if !hasPodGet {
		t.Fatal("ClusterRole must allow get on core pods for warning-event filtering")
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
