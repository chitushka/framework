package performance

import (
	"encoding/json"
	"strings"
	"testing"
)

func goodReport(t *testing.T) (Protocol, SoakReport) {
	t.Helper()
	p, err := LoadProtocol()
	if err != nil {
		t.Fatal(err)
	}
	r := SoakReport{Version: p.Version, ProtocolHash: ProtocolHash(), Mode: "full", Before: Sample{HeapBytes: 1 << 20, Goroutines: 10}, Warm: Sample{HeapBytes: 2 << 20}, After: Sample{HeapBytes: 1 << 20, Goroutines: 10}, ServerBefore: 1, ServerAfter: 1, CleanupMS: 1}
	for _, phase := range p.Soak.Phases {
		result := PhaseResult{Name: phase.Name, Seconds: float64(phase.Seconds), Scheduled: phase.Seconds * p.Soak.Rate, P99MS: 1}
		if phase.Name == "pool" || phase.Name == "slow" {
			result.Unavailable = result.Scheduled
		} else {
			result.Success = result.Scheduled
		}
		r.Phases = append(r.Phases, result)
	}
	for i := range 8 {
		r.Samples = append(r.Samples, Sample{Phase: "recovery", Seconds: 85 + float64(i*5), HeapBytes: 2 << 20, Open: 4})
	}
	return p, r
}

func TestResourceGuardsFailClosed(t *testing.T) {
	p, original := goodReport(t)
	if err := ValidateSoak(p, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SoakReport){
		"missing phases":         func(r *SoakReport) { r.Phases = nil },
		"missing samples":        func(r *SoakReport) { r.Samples = nil },
		"few samples":            func(r *SoakReport) { r.Samples = r.Samples[:5] },
		"zero report":            func(r *SoakReport) { *r = SoakReport{} },
		"wrong protocol":         func(r *SoakReport) { r.ProtocolHash = "other" },
		"unobserved fault":       func(r *SoakReport) { r.Phases[2].Success = r.Phases[2].Scheduled; r.Phases[2].Unavailable = 0 },
		"short run":              func(r *SoakReport) { r.Phases[1].Seconds = 1 },
		"missing load":           func(r *SoakReport) { r.Phases[1].Scheduled = 0 },
		"slow recovery":          func(r *SoakReport) { r.Phases[4].P99MS = 1000 },
		"admission drops":        func(r *SoakReport) { r.Phases[4].Success -= 100; r.Phases[4].Dropped = 100 },
		"pool leak":              func(r *SoakReport) { r.Samples[0].Open = 5 },
		"server leak":            func(r *SoakReport) { r.ServerAfter = 2 },
		"client connection leak": func(r *SoakReport) { r.After.Open = 1 },
		"in use after phase":     func(r *SoakReport) { r.Phases[2].EndInUse = 1 },
		"phase drain timeout":    func(r *SoakReport) { r.Phases[2].DrainMS = 6000 },
		"heap leak":              func(r *SoakReport) { r.After.HeapBytes = 64 << 20 },
		"goroutine leak":         func(r *SoakReport) { r.After.Goroutines = 30 },
		"cleanup timeout":        func(r *SoakReport) { r.CleanupMS = 6000 },
		"rising heap": func(r *SoakReport) {
			for i := range r.Samples {
				r.Samples[i].HeapBytes += uint64(i) * (1 << 20)
			}
		},
		"invalid times": func(r *SoakReport) { r.Samples[2].Seconds = 0 },
		"unknown mode":  func(r *SoakReport) { r.Mode = "skip" },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			var candidate SoakReport
			if err := json.Unmarshal(encoded, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if err := ValidateSoak(p, &candidate); err == nil || !strings.Contains(err.Error(), "performance:") {
				t.Fatal("invalid resource report accepted", err)
			}
		})
	}
}
