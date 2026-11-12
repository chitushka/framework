package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/chitushka/framework/app"
)

// HealthConfig specifies a total readiness probe budget and a global concurrency
// limit, including probes that ignore cancellation. Checks must be read-only.
type HealthConfig struct {
	ProbeTimeout        time.Duration
	MaxConcurrentProbes int
	Checks              []func(context.Context) error
}

type Health struct {
	application *app.Application
	timeout     time.Duration
	checks      []func(context.Context) error
	slots       chan struct{}
}

func NewHealth(application *app.Application, config HealthConfig) (*Health, error) {
	if application == nil {
		return nil, errors.New("web: health application required")
	}
	if config.ProbeTimeout == 0 {
		config.ProbeTimeout = 250 * time.Millisecond
	}
	if config.MaxConcurrentProbes == 0 {
		config.MaxConcurrentProbes = 4
	}
	if config.ProbeTimeout < 0 || config.MaxConcurrentProbes < 1 || config.MaxConcurrentProbes > 64 || len(config.Checks) > 32 {
		return nil, errors.New("web: invalid health probe budgets (max 64 concurrent, 32 checks)")
	}
	for _, check := range config.Checks {
		if check == nil {
			return nil, errors.New("web: nil health check")
		}
	}
	return &Health{application: application, timeout: config.ProbeTimeout,
		checks: append([]func(context.Context) error(nil), config.Checks...), slots: make(chan struct{}, config.MaxConcurrentProbes)}, nil
}

// Liveness reflects process lifecycle only; an unavailable dependency never
// triggers a restart through this endpoint. Mount explicitly on a management mux.
func (h *Health) Liveness(w http.ResponseWriter, r *http.Request) {
	h.respond(w, r, h.application.Live())
}

// Readiness is false during startup, rollback and drain. Probe failures, overload
// and deadlines fail closed; responses never contain dependency error details.
func (h *Health) Readiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.respond(w, r, false)
		return
	}
	ready := h.application.Running()
	if ready {
		ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
		defer cancel()
		for _, check := range h.checks {
			if !h.probe(ctx, check) {
				ready = false
				break
			}
		}
		ready = ready && ctx.Err() == nil && h.application.Running()
	}
	h.respond(w, r, ready)
}

func (h *Health) probe(ctx context.Context, check func(context.Context) error) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case h.slots <- struct{}{}:
	default:
		return false
	}
	done := make(chan bool, 1)
	go func() {
		ok := false
		defer func() {
			if recover() != nil {
				ok = false
			}
			<-h.slots
			done <- ok
		}()
		ok = check(ctx) == nil
	}()
	select {
	case ok := <-done:
		return ok
	case <-ctx.Done():
		return false
	}
}

func (h *Health) respond(w http.ResponseWriter, r *http.Request, ok bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	status, body := http.StatusOK, "ok\n"
	if !ok {
		status, body = http.StatusServiceUnavailable, "unavailable\n"
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body))
	}
}
