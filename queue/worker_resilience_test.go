package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerRecoversHandlerPanicAndRetries(t *testing.T) {
	broker := NewMemoryBroker()
	producer, err := New(broker)
	if err != nil {
		t.Fatal(err)
	}
	failures := make(chan Failure, 1)
	processed := make(chan struct{})
	worker, err := NewWorker(broker, WorkerConfig{
		PollInterval: time.Millisecond,
		RetryDelay:   func(int) time.Duration { return 0 },
		OnFailure:    func(failure Failure) { failures <- failure },
	})
	if err != nil {
		t.Fatal(err)
	}
	broken := errors.New("handler failed")
	var calls atomic.Int32
	if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error {
		if calls.Add(1) == 1 {
			panic(broken)
		}
		close(processed)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Push(t.Context(), "job", nil, Options{MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	startResilientWorker(t, worker)
	select {
	case failure := <-failures:
		var recovered *PanicError
		if !errors.As(failure.Err, &recovered) || !errors.Is(failure.Err, broken) || failure.Final {
			t.Fatalf("panic failure = %+v", failure)
		}
	case <-time.After(time.Second):
		t.Fatal("handler panic was not reported")
	}
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("worker did not process retry after panic")
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("handler calls = %d", calls.Load())
	}
	if _, err := broker.Reserve(t.Context(), 0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("successfully retried job is still queued: %v", err)
	}
}

func TestWorkerJobDeadlineRetriesEvenWhenHandlerReturnsNil(t *testing.T) {
	for _, returnError := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil", true: "context error"}[returnError], func(t *testing.T) {
			broker := NewMemoryBroker()
			producer, err := New(broker)
			if err != nil {
				t.Fatal(err)
			}
			failures := make(chan Failure, 1)
			worker, err := NewWorker(broker, WorkerConfig{
				JobTimeout:   10 * time.Millisecond,
				PollInterval: time.Millisecond,
				RetryDelay:   func(int) time.Duration { return time.Hour },
				OnFailure:    func(failure Failure) { failures <- failure },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RegisterHandler("job", func(ctx context.Context, _ json.RawMessage) error {
				<-ctx.Done()
				if returnError {
					return ctx.Err()
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := producer.Push(t.Context(), "job", nil, Options{MaxAttempts: 2}); err != nil {
				t.Fatal(err)
			}
			startResilientWorker(t, worker)
			select {
			case failure := <-failures:
				if !errors.Is(failure.Err, context.DeadlineExceeded) || failure.Final {
					t.Fatalf("timeout failure = %+v", failure)
				}
			case <-time.After(time.Second):
				t.Fatal("handler deadline did not trigger retry")
			}
		})
	}
}

type singleDeliveryBroker struct{ deliveries chan Delivery }

func (broker *singleDeliveryBroker) Enqueue(context.Context, Message, time.Duration) error {
	return errors.New("not supported in this test")
}

func (broker *singleDeliveryBroker) Reserve(ctx context.Context, _ time.Duration) (Delivery, error) {
	select {
	case delivery := <-broker.deliveries:
		return delivery, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type deadlineDelivery struct {
	message Message
	settle  func(context.Context, bool) error
}

func (delivery *deadlineDelivery) Message() Message { return delivery.message }
func (delivery *deadlineDelivery) Ack(ctx context.Context) error {
	return delivery.settle(ctx, true)
}
func (delivery *deadlineDelivery) Nack(ctx context.Context, _ time.Duration) error {
	return delivery.settle(ctx, false)
}

func TestWorkerSettlementHasDeadline(t *testing.T) {
	for _, scenario := range []string{"success ack", "final ack", "retry nack"} {
		t.Run(scenario, func(t *testing.T) {
			broker := &singleDeliveryBroker{deliveries: make(chan Delivery, 1)}
			settled := make(chan bool, 1)
			message := Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 2}
			if scenario == "final ack" {
				message.MaxAttempts = 1
			}
			broker.deliveries <- &deadlineDelivery{message: message, settle: func(ctx context.Context, ack bool) error {
				settled <- ack
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("missing settlement deadline")
				}
				<-ctx.Done()
				return ctx.Err()
			}}
			worker, err := NewWorker(broker, WorkerConfig{SettlementTimeout: 10 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RegisterHandler("job", func(context.Context, json.RawMessage) error {
				if scenario != "success ack" {
					return errors.New("failed job")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			startResilientWorker(t, worker)
			select {
			case err := <-worker.Errors():
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("settlement failure = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("worker is blocked in settlement")
			}
			if ack := <-settled; ack != (scenario != "retry nack") {
				t.Fatalf("Ack used = %t in %s", ack, scenario)
			}
		})
	}
}

func TestWorkerSettlesAfterShutdownCancellation(t *testing.T) {
	broker := &singleDeliveryBroker{deliveries: make(chan Delivery, 1)}
	started := make(chan struct{})
	settled := make(chan error, 1)
	broker.deliveries <- &deadlineDelivery{
		message: Message{ID: "job", Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 2},
		settle: func(ctx context.Context, ack bool) error {
			if ack {
				settled <- errors.New("cancelled job acknowledged")
			} else if _, ok := ctx.Deadline(); !ok {
				settled <- errors.New("missing settlement deadline")
			} else {
				settled <- ctx.Err()
			}
			return nil
		},
	}
	worker, err := NewWorker(broker, WorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterHandler("job", func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	startResilientWorker(t, worker)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if err := worker.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-settled; err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRejectsNegativeExecutionTimeouts(t *testing.T) {
	for _, config := range []WorkerConfig{{JobTimeout: -1}, {SettlementTimeout: -1}} {
		if _, err := NewWorker(NewMemoryBroker(), config); err == nil {
			t.Fatalf("accepted negative timeout: %+v", config)
		}
	}
}

func startResilientWorker(t *testing.T, worker *Worker) {
	t.Helper()
	if err := worker.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := worker.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
}
