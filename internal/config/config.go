package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Cluster      ClusterConfig      `yaml:"cluster"`
	Publish      PublishConfig      `yaml:"publish"`
	StatusPolicy StatusPolicyConfig `yaml:"statusPolicy"`
	Components   []ComponentConfig  `yaml:"components"`
}

type ClusterConfig struct {
	ID         string           `yaml:"id"`
	Name       string           `yaml:"name"`
	Kubeconfig string           `yaml:"kubeconfig"`
	Context    string           `yaml:"context"`
	HTTP       HTTPConfig       `yaml:"http"`
	Prometheus PrometheusConfig `yaml:"prometheus"`
}

type HTTPConfig struct {
	Timeout string `yaml:"timeout"`
}

type PrometheusConfig struct {
	BaseURL string `yaml:"baseURL"`
	Timeout string `yaml:"timeout"`
}

type PublishConfig struct {
	IncludeCheckDetails bool `yaml:"includeCheckDetails"`
}

type StatusPolicyConfig struct {
	UnknownGraceRuns int    `yaml:"unknownGraceRuns"`
	StaleAfter       string `yaml:"staleAfter"`
}

type ComponentConfig struct {
	ID          string        `yaml:"id"`
	Name        string        `yaml:"name"`
	Group       string        `yaml:"group"`
	Description string        `yaml:"description"`
	UserFacing  bool          `yaml:"userFacing"`
	Checks      []CheckConfig `yaml:"checks"`
}

type CheckConfig struct {
	ID               string   `yaml:"id"`
	Type             string   `yaml:"type"`
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	Impact           string   `yaml:"impact"`
	Namespace        string   `yaml:"namespace"`
	Kind             string   `yaml:"kind"`
	ResourceName     string   `yaml:"resourceName"`
	ServiceName      string   `yaml:"serviceName"`
	LabelSelector    string   `yaml:"labelSelector"`
	MinReady         int      `yaml:"minReady"`
	URL              string   `yaml:"url"`
	ExpectedStatuses []int    `yaml:"expectedStatuses"`
	Timeout          string   `yaml:"timeout"`
	Path             string   `yaml:"path"`
	Query            string   `yaml:"query"`
	WarningBelow     *float64 `yaml:"warningBelow"`
	CriticalBelow    *float64 `yaml:"criticalBelow"`
	WarningAbove     *float64 `yaml:"warningAbove"`
	CriticalAbove    *float64 `yaml:"criticalAbove"`
	Since            string   `yaml:"since"`
	WarningCount     int      `yaml:"warningCount"`
	CriticalCount    int      `yaml:"criticalCount"`
}

const (
	CheckImpactServingPath   = "servingPath"
	CheckImpactControlPlane  = "controlPlane"
	CheckImpactDependency    = "dependency"
	CheckImpactSymptom       = "symptom"
	CheckImpactInformational = "informational"
)

func Load(path string) (*Config, error) {
	if path == "" {
		return nil, errors.New("config path is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	setDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func DurationOr(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func setDefaults(cfg *Config) {
	if cfg.Cluster.ID == "" {
		cfg.Cluster.ID = "default"
	}
	if cfg.Cluster.Name == "" {
		cfg.Cluster.Name = cfg.Cluster.ID
	}
	if cfg.Cluster.HTTP.Timeout == "" {
		cfg.Cluster.HTTP.Timeout = "10s"
	}
	if cfg.Cluster.Prometheus.Timeout == "" {
		cfg.Cluster.Prometheus.Timeout = cfg.Cluster.HTTP.Timeout
	}
	if cfg.StatusPolicy.UnknownGraceRuns <= 0 {
		cfg.StatusPolicy.UnknownGraceRuns = 2
	}
	if cfg.StatusPolicy.StaleAfter == "" {
		cfg.StatusPolicy.StaleAfter = "10m"
	}

	for componentIndex := range cfg.Components {
		component := &cfg.Components[componentIndex]
		if component.Group == "" {
			component.Group = "Other"
		}
		for checkIndex := range component.Checks {
			check := &component.Checks[checkIndex]
			if check.ID == "" {
				check.ID = fmt.Sprintf("%s-check-%d", component.ID, checkIndex+1)
			}
			if check.Name == "" {
				check.Name = check.ID
			}
			if normalized := NormalizeCheckImpact(check.Impact); normalized != "" {
				check.Impact = normalized
			}
			if check.Timeout == "" {
				check.Timeout = cfg.Cluster.HTTP.Timeout
			}
			if check.Since == "" {
				check.Since = "15m"
			}
			if check.CriticalCount == 0 {
				check.CriticalCount = 20
			}
		}
	}
}

func validate(cfg *Config) error {
	if len(cfg.Components) == 0 {
		return errors.New("at least one component is required")
	}
	for _, component := range cfg.Components {
		if component.ID == "" {
			return errors.New("component id is required")
		}
		if component.Name == "" {
			return fmt.Errorf("component %q name is required", component.ID)
		}
		for _, check := range component.Checks {
			if check.Type == "" {
				return fmt.Errorf("component %q check %q type is required", component.ID, check.ID)
			}
			if check.Impact != "" && NormalizeCheckImpact(check.Impact) == "" {
				return fmt.Errorf("component %q check %q impact %q is not supported", component.ID, check.ID, check.Impact)
			}
		}
	}
	return nil
}

func NormalizeCheckImpact(value string) string {
	normalized := ""
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch char {
		case '-', '_', ' ':
			continue
		default:
			normalized += string(char)
		}
	}
	switch normalized {
	case "":
		return ""
	case "servingpath":
		return CheckImpactServingPath
	case "controlplane":
		return CheckImpactControlPlane
	case "dependency":
		return CheckImpactDependency
	case "symptom":
		return CheckImpactSymptom
	case "informational":
		return CheckImpactInformational
	default:
		return ""
	}
}
