//go:build performance

package performance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	perf "github.com/chitushka/framework/internal/performance"
	"github.com/chitushka/framework/web"
)

type phaseMetrics struct {
	mu        sync.Mutex
	result    perf.PhaseResult
	histogram [20001]uint64 // 100us buckets, capped at 2s; bounded independently of load.
}

func (m *phaseMetrics) observe(elapsed time.Duration, kind int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch kind {
	case 200:
		m.result.Success++
	case 503:
		m.result.Unavailable++
	default:
		m.result.Unexpected++
	}
	bucket := min(len(m.histogram)-1, int(elapsed/(100*time.Microsecond)))
	m.histogram[bucket]++
}

func (m *phaseMetrics) percent(percent int) float64 {
	n := m.result.Success + m.result.Unavailable + m.result.Unexpected
	if n == 0 {
		return 0
	}
	target := (n*percent + 99) / 100
	var count uint64
	for i, value := range m.histogram {
		count += value
		if count >= uint64(target) {
			return float64(i+1) / 10
		}
	}
	return 2000
}

func resourceSnapshot(f *sqlFixture, start time.Time, phase string) (perf.Sample, error) {
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	s := perf.Sample{Phase: phase, Seconds: time.Since(start).Seconds(), HeapBytes: memory.HeapAlloc, Goroutines: runtime.NumGoroutine()}
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return s, err
	}
	fields := strings.Fields(string(statm))
	if len(fields) < 2 {
		return s, errors.New("performance: missing process RSS")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return s, err
	}
	s.RSSBytes = pages * uint64(os.Getpagesize())
	current, err := os.ReadFile("/sys/fs/cgroup/memory.current")
	if err != nil {
		return s, err
	}
	s.CgroupBytes, err = strconv.ParseUint(strings.TrimSpace(string(current)), 10, 64)
	if err != nil {
		return s, err
	}
	if f != nil {
		stats := f.app.Stats()
		s.Open, s.InUse, s.WaitCount = stats.OpenConnections, stats.InUse, stats.WaitCount
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		count, err := f.serverCount(ctx)
		s.ServerConnections = count
		return s, err
	}
	return s, nil
}

func soakHandler(f *sqlFixture, variant string, slow *atomic.Bool, p perf.Protocol) (http.Handler, error) {
	lookup := func(request *http.Request, id string) (perf.Payload, error) {
		value, err := strconv.Atoi(id)
		if err != nil {
			return perf.Payload{}, err
		}
		ctx, cancel := context.WithTimeout(request.Context(), time.Duration(p.Soak.SQLBudgetMS)*time.Millisecond)
		defer cancel()
		if slow.Load() {
			query := "SELECT pg_sleep(0.2)"
			if f.app.Dialect().Name() == db.MySQL {
				query = "SELECT SLEEP(0.2)"
			}
			if variant == "Chitu" {
				_, err = f.app.ExecContext(ctx, query)
			} else {
				_, err = f.app.DB().ExecContext(ctx, query)
			}
			if err != nil {
				return perf.Payload{}, err
			}
		}
		var got int
		var name string
		var row *sql.Row
		if variant == "Chitu" {
			row = f.app.QueryRowContext(ctx, f.query, value)
		} else {
			row = f.app.DB().QueryRowContext(ctx, f.query, value)
		}
		if err := row.Scan(&got, &name); err != nil {
			return perf.Payload{}, err
		}
		return perf.Payload{ID: strconv.Itoa(got), Name: name}, nil
	}
	if variant == "NetHTTP" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
			payload, err := lookup(r, r.PathValue("id"))
			if err != nil {
				perf.WriteJSON(w, 503, struct {
					Error string `json:"error"`
				}{"unavailable"})
				return
			}
			perf.WriteJSON(w, 200, payload)
		})
		return mux, nil
	}
	router := web.NewRouter()
	if err := router.GET("/items/{id}", func(c *web.Context) error {
		payload, err := lookup(c.Request, c.Param("id"))
		if err != nil {
			return c.JSON(503, struct {
				Error string `json:"error"`
			}{"unavailable"})
		}
		return c.JSON(200, payload)
	}); err != nil {
		return nil, err
	}
	return router, nil
}

func request(client *http.Client, url string, id int, metrics *phaseMetrics) {
	started := time.Now()
	kind := 0
	defer func() { metrics.observe(time.Since(started), kind) }()
	response, err := client.Get(url + "/items/" + strconv.Itoa(id))
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return
	}
	if response.StatusCode == 503 {
		kind = 503
		return
	}
	var payload perf.Payload
	if response.StatusCode == 200 && json.Unmarshal(body, &payload) == nil && payload.ID == strconv.Itoa(id) && payload.Name == perf.Name() {
		kind = 200
	}
}

func runSoak(t *testing.T, dialect, variant string, p perf.Protocol) (report perf.SoakReport, runError error) {
	mode := "full"
	if os.Getenv("PERF_SMOKE") == "1" {
		mode = "smoke"
	}
	start := time.Now()
	before, err := resourceSnapshot(nil, start, "before")
	if err != nil {
		return report, err
	}
	report = perf.SoakReport{Version: p.Version, ProtocolHash: perf.ProtocolHash(), Mode: mode, Dialect: dialect, Variant: variant, Before: before}
	f := newSQL(t, dialect)
	report.ServerBefore = f.serverBefore
	var slow atomic.Bool
	handler, err := soakHandler(f, variant, &slow, p)
	if err != nil {
		return report, err
	}
	server := httptest.NewServer(handler)
	transport := &http.Transport{MaxIdleConns: p.Soak.Inflight, MaxIdleConnsPerHost: p.Soak.Inflight, MaxConnsPerHost: p.Soak.Inflight, IdleConnTimeout: time.Minute}
	client := &http.Client{Transport: transport, Timeout: time.Duration(p.Soak.ClientTimeoutMS) * time.Millisecond}
	defer transport.CloseIdleConnections()
	defer server.Close()
	allMetrics := make([]phaseMetrics, len(p.Soak.Phases))
	var observations []error
	for phaseIndex, phase := range p.Soak.Phases {
		metrics := &allMetrics[phaseIndex]
		metrics.result.Name = phase.Name
		duration := time.Duration(phase.Seconds) * time.Second
		sampleEvery := time.Duration(p.Soak.SampleMS) * time.Millisecond
		if mode == "smoke" {
			duration /= 10
			sampleEvery /= 10
		}
		slow.Store(phase.Name == "slow")
		var pinned []*sql.Conn
		if phase.Name == "pool" {
			for range p.SQL.Pool {
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				c, err := f.app.DB().Conn(ctx)
				cancel()
				if err != nil {
					for _, old := range pinned {
						_ = old.Close()
					}
					return report, err
				}
				pinned = append(pinned, c)
			}
		}
		phaseStart := time.Now()
		nextSample := phaseStart.Add(sampleEvery)
		count := int(duration.Seconds() * float64(p.Soak.Rate))
		var requests sync.WaitGroup
		slots := make(chan struct{}, p.Soak.Inflight)
		for i := range count {
			target := phaseStart.Add(time.Duration(i) * time.Second / time.Duration(p.Soak.Rate))
			if pause := time.Until(target); pause > 0 {
				timer := time.NewTimer(pause)
				select {
				case <-timer.C:
				case <-t.Context().Done():
					timer.Stop()
					return report, t.Context().Err()
				}
			}
			metrics.result.Scheduled++
			select {
			case slots <- struct{}{}:
				requests.Add(1)
				go func(id int) {
					defer requests.Done()
					defer func() { <-slots }()
					request(client, server.URL, id, metrics)
				}(i%p.SQL.Rows + 1)
			default:
				metrics.result.Dropped++
			}
			if !time.Now().Before(nextSample) {
				sample, err := resourceSnapshot(f, start, phase.Name)
				if err != nil {
					observations = append(observations, err)
				}
				report.Samples = append(report.Samples, sample)
				nextSample = nextSample.Add(sampleEvery)
			}
		}
		if pause := time.Until(phaseStart.Add(duration)); pause > 0 {
			time.Sleep(pause)
		}
		requests.Wait()
		drainStart := time.Now()
		for _, c := range pinned {
			observations = append(observations, c.Close())
		}
		drainContext, stopDrain := context.WithTimeout(t.Context(), time.Duration(p.Soak.CleanupMS)*time.Millisecond)
		for f.app.Stats().InUse != 0 {
			select {
			case <-drainContext.Done():
				observations = append(observations, drainContext.Err())
			case <-time.After(time.Millisecond):
				continue
			}
			break
		}
		stopDrain()
		metrics.result.DrainMS = float64(time.Since(drainStart)) / float64(time.Millisecond)
		metrics.result.Seconds = time.Since(phaseStart).Seconds()
		metrics.result.EndInUse = f.app.Stats().InUse
		metrics.result.P50MS, metrics.result.P95MS, metrics.result.P99MS = metrics.percent(50), metrics.percent(95), metrics.percent(99)
		report.Phases = append(report.Phases, metrics.result)
		sample, err := resourceSnapshot(f, start, phase.Name)
		report.Samples = append(report.Samples, sample)
		observations = append(observations, err)
		if phase.Name == "warmup" {
			report.Warm = sample
		}
	}
	cleanupStart := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.Soak.CleanupMS)*time.Millisecond)
	observations = append(observations, server.Config.Shutdown(ctx))
	cancel()
	server.Close()
	transport.CloseIdleConnections()
	done := make(chan error, 1)
	go func() { done <- f.close() }()
	select {
	case err := <-done:
		observations = append(observations, err)
	case <-time.After(time.Duration(p.Soak.CleanupMS) * time.Millisecond):
		return report, errors.New("performance: SQL cleanup timed out")
	}
	report.ServerAfter = f.serverAfter
	after, err := resourceSnapshot(nil, start, "after")
	observations = append(observations, err)
	after.Open, after.InUse = f.app.Stats().OpenConnections, f.app.Stats().InUse
	report.After = after
	report.CleanupMS = float64(time.Since(cleanupStart)) / float64(time.Millisecond)
	observations = append(observations, perf.ValidateSoak(p, &report))
	return report, errors.Join(observations...)
}

func TestPerformanceSoak(t *testing.T) {
	p := protocol(t)
	for _, dialect := range []string{db.PostgreSQL, db.MySQL} {
		for _, variant := range []string{"NetHTTP", "Chitu"} {
			t.Run(dialect+"/"+variant, func(t *testing.T) {
				report, err := runSoak(t, dialect, variant, p)
				report.Accepted = err == nil
				if err != nil {
					report.Error = err.Error()
				}
				writeJSON(t, "soak-"+dialect+"-"+variant+".json", report)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("%s/%s: %d phases, %d samples, heap slope %.0f B/min, goroutines %d -> %d, server connections %d -> %d", dialect, variant, len(report.Phases), len(report.Samples), report.RecoverySlope, report.Before.Goroutines, report.After.Goroutines, report.ServerBefore, report.ServerAfter)
			})
		}
	}
}
