package status

import "time"

type Level string

const (
	Operational Level = "operational"
	Unknown     Level = "unknown"
	Degraded    Level = "degraded"
	Outage      Level = "outage"
)

type Cluster struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Snapshot struct {
	Version       string      `json:"version"`
	Cluster       Cluster     `json:"cluster"`
	GeneratedAt   time.Time   `json:"generatedAt"`
	OverallStatus Level       `json:"overallStatus"`
	Components    []Component `json:"components"`
}

type Component struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Group        string              `json:"group"`
	Description  string              `json:"description,omitempty"`
	UserFacing   bool                `json:"userFacing"`
	Status       Level               `json:"status"`
	Summary      string              `json:"summary"`
	PublicChecks []PublicCheckResult `json:"publicChecks,omitempty"`
	Checks       []CheckResult       `json:"checks,omitempty"`
}

type CheckResult struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Status     Level             `json:"status"`
	Message    string            `json:"message"`
	ObservedAt time.Time         `json:"observedAt"`
	DurationMS int64             `json:"durationMs"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type PublicCheckResult struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	Status        Level             `json:"status"`
	Message       string            `json:"message"`
	ReasonCode    string            `json:"reasonCode,omitempty"`
	ImpactHint    string            `json:"impactHint,omitempty"`
	SignalSummary string            `json:"signalSummary,omitempty"`
	Confidence    string            `json:"confidence,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func Combine(levels ...Level) Level {
	result := Operational
	for _, level := range levels {
		result = Worse(result, level)
	}
	return result
}

func Worse(a, b Level) Level {
	if severity(b) > severity(a) {
		return b
	}
	return a
}

func severity(level Level) int {
	switch level {
	case Operational:
		return 0
	case Unknown:
		return 1
	case Degraded:
		return 2
	case Outage:
		return 3
	default:
		return 1
	}
}
