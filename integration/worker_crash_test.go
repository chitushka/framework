package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chitushka/framework/queue"
)

type crashBroker struct {
	queue.Broker
	ready string
}

func (b crashBroker) Reserve(ctx context.Context, wait time.Duration) (queue.Delivery, error) {
	delivery, err := b.Broker.Reserve(ctx, wait)
	if err != nil {
		return nil, err
	}
	return crashDelivery{Delivery: delivery, ready: b.ready}, nil
}

type crashDelivery struct {
	queue.Delivery
	ready string
}

func (d crashDelivery) Ack(context.Context) error {
	if err := os.WriteFile(d.ready, []byte(d.Message().ID), 0o600); err != nil {
		return err
	}
	select {}
}

func TestWorkerCrashProcessHelper(t *testing.T) {
	if os.Getenv("CHITU_QUEUE_MODE") != "after-save" {
		return
	}
	broker, closeBroker := persistentBroker(t, required(t, "CHITU_QUEUE_KIND"), required(t, "CHITU_QUEUE_TABLE"), required(t, "CHITU_QUEUE_NAMESPACE"))
	defer closeBroker()

	store, err := queue.NewFileDeadLetters(required(t, "CHITU_QUEUE_DEADLETTERS"))
	check(t, err)
	defer func() { _ = store.Close() }()
	worker, err := queue.NewWorker(crashBroker{Broker: broker, ready: required(t, "CHITU_QUEUE_READY")}, queue.WorkerConfig{DeadLetters: store, PollInterval: 5 * time.Millisecond, JobTimeout: time.Second, SettlementTimeout: 2 * time.Second})
	check(t, err)
	check(t, worker.RegisterHandler("poison", func(context.Context, json.RawMessage) error { return errors.New("poison retained before Ack") }))
	check(t, worker.Start(testContext(t)))
	select {}
}

func TestPersistentQueueConsumerKillAndDeadLetterCrash(t *testing.T) {
	for _, kind := range []string{"redis", "rabbit"} {
		t.Run(kind, func(t *testing.T) {
			for _, mode := range []string{"die", "after-save"} {
				t.Run(mode, func(t *testing.T) {
					ctx := testContext(t)
					name, namespace := unique(), unique()
					broker, closeBroker := persistentBroker(t, kind, name, namespace)
					t.Cleanup(closeBroker)
					if kind == "rabbit" {
						t.Cleanup(func() { cleanupRabbitQueue(t, name) })
					}
					producer, err := queue.New(broker)
					check(t, err)
					id, err := producer.Push(ctx, "poison", nil, queue.Options{MaxAttempts: 1})
					check(t, err)
					root := t.TempDir()
					ready, directory := filepath.Join(root, "ready"), filepath.Join(root, "dead")
					helper := "^TestQueueProcessHelper$"
					if mode == "after-save" {
						helper = "^TestWorkerCrashProcessHelper$"
					}
					command := exec.CommandContext(ctx, os.Args[0], "-test.run="+helper)
					command.Env = append(os.Environ(), "CHITU_QUEUE_MODE="+mode, "CHITU_QUEUE_KIND="+kind, "CHITU_QUEUE_TABLE="+name, "CHITU_QUEUE_NAMESPACE="+namespace, "CHITU_QUEUE_READY="+ready, "CHITU_QUEUE_DEADLETTERS="+directory)
					check(t, command.Start())
					defer func() {
						if command.ProcessState == nil {
							_ = command.Process.Kill()
							_ = command.Wait()
						}
					}()
					for {
						if _, err := os.Stat(ready); err == nil {
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal("child did not reach crash point")
						case <-time.After(10 * time.Millisecond):
						}
					}
					check(t, command.Process.Kill())
					_ = command.Wait()
					store, err := queue.NewFileDeadLetters(directory)
					check(t, err)
					defer func() { _ = store.Close() }()
					if mode == "after-save" {
						record, err := store.Get(ctx, id)
						check(t, err)
						if record.Error != "poison retained before Ack" {
							t.Fatalf("record lost at crash: %+v", record)
						}
					}
					final := make(chan queue.Failure, 1)
					worker, err := queue.NewWorker(broker, queue.WorkerConfig{DeadLetters: store, PollInterval: 5 * time.Millisecond, JobTimeout: time.Second, SettlementTimeout: 2 * time.Second, OnFailure: func(f queue.Failure) { final <- f }})
					check(t, err)
					check(t, worker.RegisterHandler("poison", func(context.Context, json.RawMessage) error { return errors.New("poison after restart") }))
					check(t, worker.Start(ctx))
					select {
					case failure := <-final:
						if failure.Message.ID != id || !failure.Final {
							t.Fatal("wrong recovered job")
						}
					case err := <-worker.Errors():
						t.Fatal(err)
					case <-ctx.Done():
						t.Fatal("accepted job was lost after consumer kill")
					}
					check(t, worker.Shutdown(ctx))
					records, err := store.List(ctx, 10)
					check(t, err)
					if len(records) != 1 {
						t.Fatalf("idempotent dead-letter count=%d", len(records))
					}
					newID, err := queue.Replay(ctx, store, id, broker)
					check(t, err)
					delivery, err := broker.Reserve(ctx, time.Second)
					check(t, err)
					if delivery.Message().ID != newID || delivery.Message().Attempt != 0 {
						t.Fatal("invalid replay")
					}
					check(t, delivery.Ack(ctx))
				})
			}
		})
	}
}
