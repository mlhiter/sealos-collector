package status

import (
	"testing"
	"time"
)

func TestCombineUsesWorstStatus(t *testing.T) {
	tests := []struct {
		name   string
		levels []Level
		want   Level
	}{
		{name: "all operational", levels: []Level{Operational, Operational}, want: Operational},
		{name: "unknown beats operational", levels: []Level{Operational, Unknown}, want: Unknown},
		{name: "degraded beats unknown", levels: []Level{Unknown, Degraded}, want: Degraded},
		{name: "outage beats degraded", levels: []Level{Operational, Degraded, Outage}, want: Outage},
		{name: "empty is operational", levels: nil, want: Operational},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Combine(tt.levels...); got != tt.want {
				t.Fatalf("Combine() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSnapshotSetFreshnessRoundsDurations(t *testing.T) {
	snapshot := &Snapshot{}

	snapshot.SetFreshness(61*time.Second, 181*time.Second+time.Millisecond)

	if snapshot.Freshness == nil {
		t.Fatal("Freshness = nil, want contract")
	}
	if snapshot.Freshness.ExpectedIntervalSeconds != 61 {
		t.Fatalf("ExpectedIntervalSeconds = %d, want 61", snapshot.Freshness.ExpectedIntervalSeconds)
	}
	if snapshot.Freshness.MaxAgeSeconds != 182 {
		t.Fatalf("MaxAgeSeconds = %d, want ceil seconds", snapshot.Freshness.MaxAgeSeconds)
	}
	if got := snapshot.MaxAge(5 * time.Minute); got != 182*time.Second {
		t.Fatalf("MaxAge() = %s, want 182s", got)
	}
}

func TestSnapshotMaxAgeUsesFallbackWhenContractMissing(t *testing.T) {
	snapshot := &Snapshot{}

	if got := snapshot.MaxAge(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("MaxAge() = %s, want fallback", got)
	}
}
