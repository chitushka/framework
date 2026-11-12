//go:build !windows

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	telemetry "github.com/chitushka/framework/adapters/otel"
	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/event"
	"github.com/chitushka/framework/observability"
	"github.com/chitushka/framework/queue"
	"github.com/chitushka/framework/web"
)

func lifecycleFile(ctx context.Context, path string) error {
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

type lifecycleMarker struct{ path string }

func (m lifecycleMarker) ID() string                                   { return "quiesce-marker" }
func (m lifecycleMarker) Register(*app.Application) error              { return nil }
func (m lifecycleMarker) Boot(context.Context, *app.Application) error { return nil }
func (m lifecycleMarker) Shutdown(context.Context) error               { return nil }
func (m lifecycleMarker) Quiesce(context.Context) error {
	return os.WriteFile(m.path, []byte("ok"), 0600)
}

func TestLifecycleProcessHelper(t *testing.T) {
	mode := os.Getenv("CHITU_LIFECYCLE_MODE")
	if mode == "" {
		return
	}
	dir := required(t, "CHITU_LIFECYCLE_DIR")
	file := func(name string) string { return filepath.Join(dir, name) }
	mark := func(name string) { check(t, os.WriteFile(file(name), []byte("ok"), 0600)) }
	// Parent owns queue cleanup; child must leave unreserved jobs intact.
	broker, _ := persistentBroker(t, "redis", required(t, "CHITU_LIFECYCLE_QUEUE"), "lifecycle")
	connection, err := db.Open(t.Context(), db.Config{Driver: "pgx", Dialect: db.PostgreSQL, DSN: required(t, "POSTGRES_DSN")}, db.NewDefaultDialectRegistry())
	check(t, err)
	defer func() { check(t, connection.Close()) }()
	provider, err := telemetry.NewHTTP(t.Context(), telemetry.Config{ServiceName: "lifecycle-process", SampleRatio: 1, BatchInterval: time.Minute}, telemetry.HTTPConfig{Endpoint: required(t, "OTEL_TEST_ENDPOINT"), Insecure: true})
	check(t, err)
	a := app.New(nil, nil)
	check(t, a.SetShutdownTimeout(2*time.Second))
	finalizer, err := app.NewFinalizer("exporter", func(ctx context.Context) error {
		if mode == "hung-exporter" {
			mark("exporter-entered")
			select {}
		}
		err := provider.Shutdown(ctx)
		if err == nil {
			if stats := provider.Stats(); stats.Exported < 2 || stats.Unexported != 0 {
				return fmt.Errorf("undrained exporter: %+v", stats)
			}
			mark("exporter-done")
		}
		return err
	})
	check(t, err)
	check(t, a.AddModule(finalizer))
	health, err := web.NewHealth(a, web.HealthConfig{Checks: []func(context.Context) error{connection.DB().PingContext}})
	check(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/live", health.Liveness)
	mux.HandleFunc("/ready", health.Readiness)
	management, err := web.NewServer(web.ServerConfig{Address: "127.0.0.1:0"}, mux, web.WithServerID("management"))
	check(t, err)
	check(t, a.AddModule(management))
	check(t, a.AddModule(lifecycleMarker{path: file("quiesced")}))
	worker, err := queue.NewWorker(broker, queue.WorkerConfig{Observer: provider, PollInterval: 5 * time.Millisecond, DrainTimeout: time.Second, ShutdownTimeout: 30 * time.Second, JobTimeout: time.Minute})
	check(t, err)
	check(t, worker.RegisterHandler("active", func(ctx context.Context, _ json.RawMessage) error {
		mark("job-started")
		if mode == "hung-worker" {
			select {}
		}
		if err := lifecycleFile(ctx, file("release-job")); err != nil {
			return err
		}
		mark("job-done")
		return nil
	}))
	check(t, a.AddModule(worker))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/slow" {
			w.WriteHeader(204)
			return
		}
		mark("http-started")
		if mode == "hung-http" {
			select {}
		}
		if lifecycleFile(r.Context(), file("release-http")) != nil {
			return
		}
		mark("http-done")
		w.WriteHeader(204)
	})
	server, err := web.NewServer(web.ServerConfig{Address: "127.0.0.1:0", ShutdownTimeout: 30 * time.Second}, observability.HTTP(observability.HTTPConfig{Tracer: provider})(handler))
	check(t, err)
	check(t, a.AddModule(server))
	_, err = a.Events.Subscribe(app.EventAfterStart, 0, func(context.Context, event.Event) error {
		data, err := json.Marshal([]string{management.Address(), server.Address()})
		if err != nil {
			return err
		}
		if err := os.WriteFile(file("addresses.tmp"), data, 0600); err != nil {
			return err
		}
		return os.Rename(file("addresses.tmp"), file("addresses"))
	})
	check(t, err)
	_, err = a.Events.Subscribe(app.EventBeforeStop, 0, func(ctx context.Context, _ event.Event) error {
		mark("stopping")
		return lifecycleFile(ctx, file("release-stop"))
	})
	check(t, err)
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	err = a.Run(ctx)
	if mode == "graceful" {
		check(t, err)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected shutdown deadline, got %v", err)
	}
	mark("exited")
}

func TestLifecycleSIGTERMDrainsOrExitsWithinSharedBudget(t *testing.T) {
	waitCollector(t)
	for _, mode := range []string{"graceful", "hung-http", "hung-worker", "hung-exporter"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			file := func(name string) string { return filepath.Join(dir, name) }
			mark := func(name string) { check(t, os.WriteFile(file(name), []byte("ok"), 0600)) }
			wait := func(name string) {
				if err := lifecycleFile(ctx, file(name)); err != nil {
					data, _ := os.ReadFile(file("output"))
					t.Fatalf("waiting for %s: %v\n%s", name, err, data)
				}
			}
			name := unique()
			broker, closeBroker := persistentBroker(t, "redis", name, "lifecycle")
			defer closeBroker()
			producer, err := queue.New(broker)
			check(t, err)
			_, err = producer.Push(ctx, "active", nil, queue.Options{})
			check(t, err)
			output, err := os.Create(file("output"))
			check(t, err)
			defer func() { check(t, output.Close()) }()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLifecycleProcessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "CHITU_LIFECYCLE_MODE="+mode, "CHITU_LIFECYCLE_DIR="+dir, "CHITU_LIFECYCLE_QUEUE="+name)
			cmd.Stdout = output
			cmd.Stderr = output
			check(t, cmd.Start())
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			wait("addresses")
			data, err := os.ReadFile(file("addresses"))
			check(t, err)
			var addresses []string
			check(t, json.Unmarshal(data, &addresses))
			client := &http.Client{Timeout: 5 * time.Second}
			status := func(address, path string) int {
				request, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+path, nil)
				check(t, err)
				response, err := client.Do(request)
				check(t, err)
				check(t, response.Body.Close())
				return response.StatusCode
			}
			if status(addresses[0], "/ready") != 200 {
				t.Fatal("not ready after boot")
			}
			traceID := fmt.Sprintf("%x%x", randomBytes(), randomBytes())
			request, err := http.NewRequestWithContext(ctx, "GET", "http://"+addresses[1]+"/slow", nil)
			check(t, err)
			request.Header.Set("traceparent", "00-"+traceID+"-0011223344556677-01")
			httpDone := make(chan error, 1)
			go func() {
				response, err := client.Do(request)
				if err == nil {
					err = response.Body.Close()
					if response.StatusCode != 204 {
						err = fmt.Errorf("HTTP status %d", response.StatusCode)
					}
				}
				httpDone <- err
			}()
			wait("job-started")
			wait("http-started")
			start := time.Now()
			check(t, cmd.Process.Signal(syscall.SIGTERM))
			wait("stopping")
			if status(addresses[0], "/ready") != 503 || status(addresses[0], "/live") != 200 || status(addresses[1], "/ping") != 204 {
				t.Fatal("readiness not withdrawn before admission stopped")
			}
			mark("release-stop")
			wait("quiesced")
			queued, err := producer.Push(ctx, "active", nil, queue.Options{})
			check(t, err)
			if mode != "hung-http" {
				mark("release-http")
			}
			if mode != "hung-worker" {
				mark("release-job")
			}
			select {
			case err := <-done:
				if err != nil {
					data, _ := os.ReadFile(file("output"))
					t.Fatalf("child: %v\n%s", err, data)
				}
			case <-ctx.Done():
				t.Fatal("process failed to exit", ctx.Err())
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("shared 2s budget exceeded (including race runtime exit): %v", elapsed)
			}
			wait("exited")
			if mode == "graceful" {
				check(t, <-httpDone)
				wait("http-done")
				wait("job-done")
				wait("exporter-done")
				delivery, err := broker.Reserve(ctx, time.Second)
				check(t, err)
				if delivery.Message().ID != queued {
					t.Fatal("active job not acknowledged or worker reserved after quiesce")
				}
				check(t, delivery.Ack(ctx))
				collectorSpans(t, traceID, 1)
			}
		})
	}
}
