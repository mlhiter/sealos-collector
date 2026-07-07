package status

import "testing"

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
