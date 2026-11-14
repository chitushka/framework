package performance

import (
	"errors"
	"fmt"
	"math"
)

type Sample struct {
	Phase             string  `json:"phase"`
	Seconds           float64 `json:"seconds"`
	HeapBytes         uint64  `json:"heapBytes"`
	RSSBytes          uint64  `json:"rssBytes"`
	CgroupBytes       uint64  `json:"cgroupBytes"`
	Goroutines        int     `json:"goroutines"`
	Open              int     `json:"open"`
	InUse             int     `json:"inUse"`
	WaitCount         int64   `json:"waitCount"`
	ServerConnections int     `json:"serverConnections"`
}

type PhaseResult struct {
	Name        string  `json:"name"`
	Seconds     float64 `json:"seconds"`
	Scheduled   int     `json:"scheduled"`
	Success     int     `json:"success"`
	Unavailable int     `json:"unavailable"`
	Unexpected  int     `json:"unexpected"`
	Dropped     int     `json:"dropped"`
	P50MS       float64 `json:"p50MS"`
	P95MS       float64 `json:"p95MS"`
	P99MS       float64 `json:"p99MS"`
	EndInUse    int     `json:"endInUse"`
	DrainMS     float64 `json:"drainMS"`
}

type SoakReport struct {
	Version       string        `json:"version"`
	ProtocolHash  string        `json:"protocolHash"`
	Mode          string        `json:"mode"`
	Dialect       string        `json:"dialect"`
	Variant       string        `json:"variant"`
	Before        Sample        `json:"before"`
	Warm          Sample        `json:"warm"`
	After         Sample        `json:"after"`
	Samples       []Sample      `json:"samples"`
	Phases        []PhaseResult `json:"phases"`
	ServerBefore  int           `json:"serverBefore"`
	ServerAfter   int           `json:"serverAfter"`
	CleanupMS     float64       `json:"cleanupMS"`
	RecoverySlope float64       `json:"recoverySlopeBytesPerMinute"`
	Accepted      bool          `json:"accepted"`
	Error         string        `json:"error,omitempty"`
}

// HeapSlope is OLS bytes/minute, with elapsed time centered for stability.
func HeapSlope(samples []Sample) (float64, error) {
	if len(samples) < 6 {
		return 0, errors.New("performance: insufficient recovery samples")
	}
	samples = samples[len(samples)-6:]
	var meanX, meanY float64
	for i, s := range samples {
		if math.IsNaN(s.Seconds) || math.IsInf(s.Seconds, 0) || (i > 0 && s.Seconds <= samples[i-1].Seconds) {
			return 0, errors.New("performance: invalid sample times")
		}
		meanX += s.Seconds / 6
		meanY += float64(s.HeapBytes) / 6
	}
	var covariance, variance float64
	for _, s := range samples {
		x := s.Seconds - meanX
		covariance += x * (float64(s.HeapBytes) - meanY)
		variance += x * x
	}
	return covariance / variance * 60, nil
}

// ValidateSoak rejects incomplete observations as well as operational violations.
func ValidateSoak(p Protocol, r *SoakReport) error {
	var failures []error
	fail := func(message string) { failures = append(failures, errors.New("performance: "+message)) }
	if r.ProtocolHash != ProtocolHash() || r.Version != p.Version || len(r.Phases) != len(p.Soak.Phases) {
		fail("protocol or phase count differs")
	}
	for i, phase := range r.Phases {
		if i >= len(p.Soak.Phases) || phase.Name != p.Soak.Phases[i].Name || phase.Scheduled <= 0 ||
			phase.Success < 0 || phase.Unavailable < 0 || phase.Unexpected < 0 || phase.Dropped < 0 ||
			math.IsNaN(phase.Seconds) || math.IsInf(phase.Seconds, 0) || math.IsNaN(phase.P99MS) || math.IsInf(phase.P99MS, 0) || phase.P99MS < 0 ||
			phase.Success+phase.Unavailable+phase.Unexpected+phase.Dropped != phase.Scheduled {
			fail("incomplete phase counts")
			continue
		}
		seconds := float64(p.Soak.Phases[i].Seconds)
		if r.Mode == "smoke" {
			seconds /= 10
		}
		if phase.Seconds < seconds || phase.Scheduled != int(math.Ceil(seconds*float64(p.Soak.Rate))) || phase.EndInUse != 0 || math.IsNaN(phase.DrainMS) || math.IsInf(phase.DrainMS, 0) || phase.DrainMS < 0 || phase.DrainMS > float64(p.Soak.CleanupMS) {
			fail("phase duration/load/drain differs: " + phase.Name)
		}
		if phase.Name == "warmup" {
			continue
		}
		percent := func(n int) float64 { return 100 * float64(n) / float64(phase.Scheduled) }
		budget := p.Soak.Budget
		if phase.Name == "pool" || phase.Name == "slow" {
			if percent(phase.Unavailable) < budget.Fault503MinPercent {
				fail("fault was not observed: " + phase.Name)
			}
		} else if percent(phase.Success) < budget.SuccessMinPercent || percent(phase.Dropped) > budget.DropMaxPercent || phase.P99MS > budget.P99MS || math.IsNaN(phase.P99MS) {
			fail("healthy throughput/latency exceeds budget: " + phase.Name)
		}
		if percent(phase.Unexpected) > budget.UnexpectedMaxPercent {
			fail("unexpected responses: " + phase.Name)
		}
	}
	var recovery []Sample
	for i, sample := range r.Samples {
		if sample.Seconds <= 0 || math.IsNaN(sample.Seconds) || math.IsInf(sample.Seconds, 0) || (i > 0 && sample.Seconds <= r.Samples[i-1].Seconds) {
			fail("invalid resource sample times")
		}
		if sample.Open > p.SQL.Pool || sample.InUse > sample.Open || sample.Open < 0 || sample.InUse < 0 {
			fail("pool limit exceeded")
		}
		if sample.Phase == "recovery" {
			recovery = append(recovery, sample)
		}
	}
	if len(recovery) == 0 || r.Warm.HeapBytes == 0 || r.Before.HeapBytes == 0 || r.After.HeapBytes == 0 {
		fail("missing resource observations")
	} else {
		for _, sample := range r.Samples {
			if sample.Phase != "warmup" && int64(sample.HeapBytes)-int64(r.Warm.HeapBytes) > p.Soak.Budget.HeapGrowthBytes {
				fail("heap growth exceeds budget")
			}
		}
		slope, err := HeapSlope(recovery)
		r.RecoverySlope = slope
		if err != nil {
			failures = append(failures, err)
		} else if r.Mode != "smoke" && slope > p.Soak.Budget.SlopeBytesPerMinute {
			fail("recovery heap slope exceeds budget")
		}
	}
	if r.After.Open != 0 || r.After.InUse != 0 || r.ServerBefore < 1 || r.ServerAfter > r.ServerBefore || r.ServerAfter < 1 {
		fail("connections remained after cleanup")
	}
	if r.CleanupMS > float64(p.Soak.CleanupMS) || r.CleanupMS <= 0 || math.IsNaN(r.CleanupMS) ||
		r.After.Goroutines > r.Before.Goroutines+p.Soak.Budget.Goroutines || int64(r.After.HeapBytes)-int64(r.Before.HeapBytes) > p.Soak.Budget.HeapGrowthBytes {
		fail("cleanup resource budget exceeded")
	}
	if r.Mode != "full" && r.Mode != "smoke" {
		fail(fmt.Sprintf("unknown run mode %q", r.Mode))
	}
	return errors.Join(failures...)
}
