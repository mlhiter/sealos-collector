package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mlhiter/sealos-collector/internal/config"
	"github.com/mlhiter/sealos-collector/internal/status"
)

type StateStore struct {
	path  string
	state persistedState
	dirty bool
}

type persistedState struct {
	Version string                `json:"version"`
	Checks  map[string]checkState `json:"checks"`
}

type checkState struct {
	LastStatus    status.Level `json:"lastStatus"`
	LastMessage   string       `json:"lastMessage"`
	LastObserved  time.Time    `json:"lastObserved"`
	UnknownStreak int          `json:"unknownStreak"`
	LastUnknown   time.Time    `json:"lastUnknown,omitempty"`
}

func LoadStateStore(path string) (*StateStore, error) {
	store := &StateStore{
		path: path,
		state: persistedState{
			Version: "v1",
			Checks:  map[string]checkState{},
		},
	}
	if path == "" {
		return store, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store, nil
		}
		return nil, fmt.Errorf("read state: %w", err)
	}
	if len(raw) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(raw, &store.state); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if store.state.Version == "" {
		store.state.Version = "v1"
	}
	if store.state.Checks == nil {
		store.state.Checks = map[string]checkState{}
	}
	return store, nil
}

func (s *StateStore) StabilizeUnknown(key string, result status.CheckResult, policy config.StatusPolicyConfig, now time.Time) status.CheckResult {
	if s == nil {
		return result
	}
	if result.Status != status.Unknown {
		s.state.Checks[key] = checkState{
			LastStatus:   result.Status,
			LastMessage:  result.Message,
			LastObserved: result.ObservedAt,
		}
		s.dirty = true
		return result
	}

	state := s.state.Checks[key]
	state.UnknownStreak++
	state.LastUnknown = result.ObservedAt
	s.state.Checks[key] = state
	s.dirty = true

	staleAfter := config.DurationOr(policy.StaleAfter, 10*time.Minute)
	if staleAfter <= 0 {
		staleAfter = 10 * time.Minute
	}
	graceRuns := policy.UnknownGraceRuns
	if graceRuns <= 0 {
		graceRuns = 2
	}

	metadata := cloneMetadata(result.Metadata)
	metadata["signal"] = "unknown"
	metadata["unknownStreak"] = strconv.Itoa(state.UnknownStreak)

	hasRecentKnown := state.LastStatus != "" &&
		state.LastStatus != status.Unknown &&
		!state.LastObserved.IsZero() &&
		now.Sub(state.LastObserved) <= staleAfter
	if hasRecentKnown && state.UnknownStreak <= graceRuns {
		result.Status = state.LastStatus
		result.Message = fmt.Sprintf("using last known %s signal from %s; current check unavailable: %s", state.LastStatus, state.LastObserved.UTC().Format(time.RFC3339), result.Message)
		metadata["stabilized"] = "true"
		metadata["lastKnownStatus"] = string(state.LastStatus)
		metadata["lastKnownAt"] = state.LastObserved.UTC().Format(time.RFC3339)
		result.Metadata = metadata
		return result
	}

	result.Status = status.Degraded
	if state.LastObserved.IsZero() {
		result.Message = "health signal unavailable: " + result.Message
	} else {
		result.Message = fmt.Sprintf("health signal stale since %s: %s", state.LastObserved.UTC().Format(time.RFC3339), result.Message)
		metadata["lastKnownStatus"] = string(state.LastStatus)
		metadata["lastKnownAt"] = state.LastObserved.UTC().Format(time.RFC3339)
	}
	metadata["stabilized"] = "false"
	result.Metadata = metadata
	return result
}

func (s *StateStore) Save() error {
	if s == nil || s.path == "" || !s.dirty {
		return nil
	}
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func cloneMetadata(metadata map[string]string) map[string]string {
	cloned := map[string]string{}
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
