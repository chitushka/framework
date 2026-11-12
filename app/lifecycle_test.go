package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/event"
)

type lifecycleModule struct {
	id      string
	boot    func(context.Context) error
	stop    func(context.Context) error
	quiesce func(context.Context) error
}

func (m *lifecycleModule) ID() string                      { return m.id }
func (m *lifecycleModule) Register(*app.Application) error { return nil }
func (m *lifecycleModule) Boot(ctx context.Context, _ *app.Application) error {
	if m.boot != nil {
		return m.boot(ctx)
	}
	return nil
}
func (m *lifecycleModule) Shutdown(ctx context.Context) error {
	if m.stop != nil {
		return m.stop(ctx)
	}
	return nil
}
func (m *lifecycleModule) Quiesce(ctx context.Context) error {
	if m.quiesce != nil {
		return m.quiesce(ctx)
	}
	return nil
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCancelledBootHasIndependentRollbackDeadline(t *testing.T) {
	a := app.New(nil, nil)
	must(t, a.SetShutdownTimeout(time.Second))
	ctx, cancel := context.WithCancel(t.Context())
	var cleaned atomic.Bool
	must(t, a.AddModule(&lifecycleModule{id: "resource", stop: func(ctx context.Context) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("unbounded cleanup")
		}
		cleaned.Store(true)
		return nil
	}}))
	must(t, a.AddModule(&lifecycleModule{id: "cancel", boot: func(context.Context) error { cancel(); return context.Canceled }}))
	if err := a.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("boot: %v", err)
	}
	if !cleaned.Load() || a.Running() || a.Live() {
		t.Fatal("rollback incomplete")
	}
}

func TestShutdownSharedBudgetEvenWithUncooperativeModulesAndHooks(t *testing.T) {
	for _, hook := range []bool{false, true} {
		t.Run(map[bool]string{false: "module", true: "hook"}[hook], func(t *testing.T) {
			a := app.New(nil, nil)
			must(t, a.SetShutdownTimeout(40*time.Millisecond))
			release := make(chan struct{})
			defer close(release)
			var calls atomic.Int32
			for _, id := range []string{"first", "last"} {
				must(t, a.AddModule(&lifecycleModule{id: id, stop: func(context.Context) error { calls.Add(1); <-release; return nil }}))
			}
			if hook {
				_, err := a.Events.Subscribe(app.EventBeforeStop, 0, func(context.Context, event.Event) error { <-release; return nil })
				must(t, err)
			}
			must(t, a.Start(t.Context()))
			start := time.Now()
			err := a.Shutdown(t.Context())
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 500*time.Millisecond {
				t.Fatalf("budget: %v %v", err, time.Since(start))
			}
			if a.Running() || a.Live() {
				t.Fatal("app still live")
			}
			if !errors.Is(a.Shutdown(t.Context()), context.DeadlineExceeded) {
				t.Fatal("lost shared result")
			}
			deadline := time.After(time.Second)
			for calls.Load() != 2 {
				select {
				case <-deadline:
					t.Fatal("remaining cleanup not attempted")
				default:
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
}

func TestReadinessQuiesceReverseDrainAndFinalizerOrder(t *testing.T) {
	a := app.New(nil, nil)
	var order []string
	f, err := app.NewFinalizer("exporter", func(context.Context) error { order = append(order, "exporter"); return nil })
	must(t, err)
	must(t, a.AddModule(f))
	for _, id := range []string{"worker", "http"} {
		must(t, a.AddModule(&lifecycleModule{id: id, quiesce: func(context.Context) error {
			if a.Running() || !a.Live() {
				return errors.New("invalid drain health")
			}
			order = append(order, "quiesce:"+id)
			return nil
		}, stop: func(context.Context) error { order = append(order, "stop:"+id); return nil }}))
	}
	must(t, a.Start(t.Context()))
	must(t, a.Shutdown(t.Context()))
	want := []string{"quiesce:http", "quiesce:worker", "stop:http", "stop:worker", "exporter"}
	for i, value := range want {
		if len(order) != len(want) || order[i] != value {
			t.Fatalf("order: %v", order)
		}
	}
}

func TestConcurrentShutdownWaitsAndExternalShutdownEndsRun(t *testing.T) {
	a := app.New(nil, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	must(t, a.AddModule(&lifecycleModule{id: "block", stop: func(context.Context) error { calls.Add(1); close(entered); <-release; return nil }}))
	ready := make(chan struct{})
	_, err := a.Events.Subscribe(app.EventAfterStart, 0, func(context.Context, event.Event) error { close(ready); return nil })
	must(t, err)
	run := make(chan error, 1)
	go func() { run <- a.Run(t.Context()) }()
	<-ready
	// AfterStart precedes the running transition; wait for that transition.
	for !a.Running() {
		time.Sleep(time.Millisecond)
	}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- a.Shutdown(t.Context()) }()
	<-entered
	go func() { second <- a.Shutdown(t.Context()) }()
	select {
	case err := <-second:
		t.Fatalf("early second shutdown: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	must(t, <-first)
	must(t, <-second)
	must(t, <-run)
	if calls.Load() != 1 {
		t.Fatal("duplicate cleanup")
	}
}

func TestShutdownDuringBootCancelsAndWaitsForRollback(t *testing.T) {
	a := app.New(nil, nil)
	booting := make(chan struct{})
	must(t, a.AddModule(&lifecycleModule{id: "boot", boot: func(ctx context.Context) error { close(booting); <-ctx.Done(); return ctx.Err() }}))
	started := make(chan error, 1)
	go func() { started <- a.Start(t.Context()) }()
	<-booting
	must(t, a.Shutdown(t.Context()))
	if !errors.Is(<-started, context.Canceled) || a.Live() {
		t.Fatal("startup not cancelled")
	}
}

func TestShutdownBoundsUncooperativeBootAndRollback(t *testing.T) {
	a := app.New(nil, nil)
	must(t, a.SetShutdownTimeout(30*time.Millisecond))
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	must(t, a.AddModule(&lifecycleModule{id: "resource", stop: func(context.Context) error { <-release; return nil }}))
	must(t, a.AddModule(&lifecycleModule{id: "hung-boot", boot: func(context.Context) error { close(entered); <-release; return nil }}))
	started := make(chan error, 1)
	go func() { started <- a.Start(t.Context()) }()
	<-entered
	begin := time.Now()
	if err := a.Shutdown(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown: %v", err)
	}
	if !errors.Is(<-started, context.Canceled) || time.Since(begin) > 500*time.Millisecond {
		t.Fatal("hung boot prevented rollback")
	}
}

func TestCancelledShutdownCallerDoesNotCancelCleanup(t *testing.T) {
	a := app.New(nil, nil)
	cleaned := make(chan error, 1)
	must(t, a.AddModule(&lifecycleModule{id: "cleanup", stop: func(ctx context.Context) error { cleaned <- ctx.Err(); return nil }}))
	must(t, a.Start(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = a.Shutdown(ctx)
	must(t, <-cleaned)
	must(t, a.Shutdown(t.Context()))
}

func TestLifecycleRejectsInvalidConfigurationAndContainsCleanupPanic(t *testing.T) {
	a := app.New(nil, nil)
	if a.SetShutdownTimeout(0) == nil || a.SetShutdownTimeout(-1) == nil {
		t.Fatal("invalid budget accepted")
	}
	if _, err := app.NewFinalizer("", func(context.Context) error { return nil }); err == nil {
		t.Fatal("empty finalizer")
	}
	if _, err := app.NewFinalizer("nil", nil); err == nil {
		t.Fatal("nil finalizer")
	}
	must(t, a.AddModule(&lifecycleModule{id: "panic", stop: func(context.Context) error { panic("cleanup") }}))
	must(t, a.Start(t.Context()))
	if a.SetShutdownTimeout(time.Second) == nil {
		t.Fatal("late budget accepted")
	}
	if a.Shutdown(t.Context()) == nil {
		t.Fatal("panic lost")
	}
}

func TestLifecycleStartEventFailureRollsBack(t *testing.T) {
	for _, name := range []string{app.EventBeforeStart, app.EventAfterStart} {
		t.Run(name, func(t *testing.T) {
			a := app.New(nil, nil)
			var cleaned atomic.Bool
			must(t, a.AddModule(&lifecycleModule{id: "resource", stop: func(context.Context) error { cleaned.Store(true); return nil }}))
			want := errors.New("event failure")
			_, err := a.Events.Subscribe(name, 0, func(context.Context, event.Event) error { return want })
			must(t, err)
			if !errors.Is(a.Start(t.Context()), want) || cleaned.Load() != (name == app.EventAfterStart) || a.Live() {
				t.Fatal("event rollback")
			}
		})
	}
}
