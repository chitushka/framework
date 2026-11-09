package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryReclaimsAndFencesAbandonedDelivery(t *testing.T) {
	broker, err := NewMemoryBrokerWithLease(10 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	message := Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 1}
	if err := broker.Enqueue(t.Context(), message, 0); err != nil {
		t.Fatal(err)
	}
	old, err := broker.Reserve(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	newer, err := broker.Reserve(t.Context(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Ack(t.Context()); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("stale memory receipt accepted", err)
	}
	if err := newer.(Abandoner).Abandon(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := broker.Reserve(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if again.Message().Attempt != 0 {
		t.Fatal("abandon spent retry budget")
	}
	if err := again.Ack(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStorageFailureReleasesOriginalMessage(t *testing.T) {
	broker := NewMemoryBroker()
	producer, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	id, err := producer.Push(t.Context(), "poison", nil, Options{MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(broker, WorkerConfig{DeadLetters: failedStorage{err: errors.New("disk full")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterHandler("poison", func(context.Context, json.RawMessage) error { return errors.New("poison") }); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.Errors():
	case <-time.After(time.Second):
		t.Fatal("failure missing")
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Reserve(t.Context(), 0)
	if err != nil {
		t.Fatal("accepted job lost on storage failure", err)
	}
	if delivery.Message().ID != id || delivery.Message().Attempt != 0 {
		t.Fatal("infrastructure failure consumed handler attempts")
	}
	if err := delivery.Ack(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRetryCallbackPanicStopsWorkerAndPreservesJob(t *testing.T) {
	broker := NewMemoryBroker()
	producer, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	_, err = producer.Push(t.Context(), "job", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(broker, WorkerConfig{RetryDelay: func(int) time.Duration { panic("retry callback") }})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error { return errors.New("retry") }); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-worker.Errors():
		var recovered *PanicError
		if !errors.As(err, &recovered) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback panic not reported")
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Reserve(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Ack(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type groupFailureBroker struct {
	delivery Delivery
	started  <-chan struct{}
	calls    atomic.Int32
}

func (b *groupFailureBroker) Enqueue(context.Context, Message, time.Duration) error { return nil }
func (b *groupFailureBroker) Reserve(ctx context.Context, _ time.Duration) (Delivery, error) {
	if b.calls.Add(1) == 1 {
		return b.delivery, nil
	}
	select {
	case <-b.started:
		return nil, errors.New("broker fatal")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestFatalConsumerCancelsWholeGroup(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	broker := &groupFailureBroker{started: started, delivery: &deadlineDelivery{message: Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 3}, settle: func(context.Context, bool) error { return nil }}}
	worker, err := NewWorker(broker, WorkerConfig{Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterHandler("job", func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-worker.Errors():
	case <-time.After(time.Second):
		t.Fatal("fatal missing")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("other consumer still running")
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
