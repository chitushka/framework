package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func testDeadLetters(t *testing.T) *FileDeadLetters {
	t.Helper()
	store, err := NewFileDeadLetters(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestDeadLetterRestartAndReplayRetainsAudit(t *testing.T) {
	directory := t.TempDir()
	store, err := NewFileDeadLetters(directory)
	if err != nil {
		t.Fatal(err)
	}
	record := FailedJob{Message: Message{ID: "id/../../escape", Name: "job", Payload: json.RawMessage(`{"x":1}`), Attempt: 2, MaxAttempts: 3}, Error: "poison", FailedAt: time.Now().UTC()}
	if err := store.Save(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileDeadLetters(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	loaded, err := store.Get(t.Context(), record.Message.ID)
	if err != nil || loaded.Error != "poison" {
		t.Fatal(loaded, err)
	}
	if err := store.Save(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(t.Context(), 10)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	broker := NewMemoryBroker()
	id, err := Replay(t.Context(), store, record.Message.ID, broker)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Reserve(t.Context(), 0)
	if err != nil || delivery.Message().ID != id || delivery.Message().Attempt != 0 || id == record.Message.ID {
		t.Fatal(delivery, err)
	}
	if _, err := store.Get(t.Context(), record.Message.ID); err != nil {
		t.Fatal("replay removed audit", err)
	}
	if err := delivery.Ack(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), record.Message.ID); err != nil {
		t.Fatal(err)
	}
}

type failedStorage struct {
	DeadLetterStore
	err error
}

// Deadline branch tests isolate Ack/Nack timing from filesystem scheduling.
type immediateDeadLetters struct{ DeadLetterStore }

func (immediateDeadLetters) Save(context.Context, FailedJob) error { return nil }

func (s failedStorage) Save(context.Context, FailedJob) error { return s.err }

func TestWorkerDoesNotAckWhenDeadLetterSaveFails(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "storage failure"}[missing], func(t *testing.T) {
			broker := &singleDeliveryBroker{deliveries: make(chan Delivery, 1)}
			var acks atomic.Int32
			broker.deliveries <- &deadlineDelivery{message: Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 1}, settle: func(context.Context, bool) error { acks.Add(1); return nil }}
			config := WorkerConfig{}
			if !missing {
				config.DeadLetters = failedStorage{err: errors.New("disk unavailable")}
			}
			worker, err := NewWorker(broker, config)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error { return errors.New("poison") }); err != nil {
				t.Fatal(err)
			}
			if err := worker.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-worker.Errors():
				if missing && !errors.Is(err, ErrDeadLetterRequired) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("storage failure not reported")
			}
			if err := worker.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
			if acks.Load() != 0 {
				t.Fatal("message acknowledged without durable storage")
			}
		})
	}
}

func TestWorkerStoppingCannotRestartAndRepeatedShutdownWaits(t *testing.T) {
	broker := NewMemoryBroker()
	producer, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	_, err = producer.Push(t.Context(), "job", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(broker, WorkerConfig{ShutdownTimeout: 20 * time.Millisecond, DrainTimeout: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error { close(started); <-release; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := worker.Shutdown(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown did not time out", err)
	}
	if err := worker.Start(t.Context()); err == nil {
		t.Fatal("overlapping generation started")
	}
	if err := worker.Shutdown(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("second shutdown falsely returned success", err)
	}
	close(release)
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerDrainCompletesWithoutCancellingHandler(t *testing.T) {
	broker := NewMemoryBroker()
	producer, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	_, err = producer.Push(t.Context(), "job", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(broker, WorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	if err := worker.RegisterHandler("job", func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-release
		result <- ctx.Err()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	shutdown := make(chan error, 1)
	go func() { shutdown <- worker.Shutdown(t.Context()) }()
	close(release)
	if err := <-shutdown; err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal("drain cancelled handler", err)
	}
}

func TestDefaultBackoffIsBoundedWithJitter(t *testing.T) {
	w, err := NewWorker(NewMemoryBroker(), WorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{0, 1, 3, 1000000} {
		for range 100 {
			delay := w.config.RetryDelay(attempt)
			if delay < 500*time.Millisecond || delay > time.Minute {
				t.Fatalf("unbounded retry: %v", delay)
			}
		}
	}
}
