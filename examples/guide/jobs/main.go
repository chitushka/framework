// Command jobs processes one job with an independent cleanup budget.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/chitushka/framework/queue"
)

type job struct {
	OperationID string `json:"operationId"`
}

func process(ctx context.Context, directory string, out io.Writer) (result error) {
	deadLetters, err := queue.NewFileDeadLetters(directory)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, deadLetters.Close()) }()
	broker := queue.NewMemoryBroker() // Learning only; queued jobs do not survive restart.
	jobs, err := queue.New(broker)
	if err != nil {
		return err
	}
	worker, err := queue.NewWorker(broker, queue.WorkerConfig{
		DeadLetters: deadLetters, JobTimeout: time.Second, SettlementTimeout: time.Second,
		ShutdownTimeout: 3 * time.Second, DrainTimeout: time.Second,
	})
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	if err := worker.RegisterHandler("report", queue.JSONHandler(func(ctx context.Context, value job) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// In a real consumer, claim OperationID with a UNIQUE constraint in the
		// same SQL transaction as the business change before sending side effects.
		_, err := fmt.Fprintln(out, "processed", value.OperationID)
		select {
		case done <- err:
		default:
		}
		return err
	})); err != nil {
		return err
	}
	if _, err := jobs.Push(ctx, "report", job{OperationID: "monthly-report"}, queue.Options{MaxAttempts: 3}); err != nil {
		return err
	}
	if err := worker.Start(ctx); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		result = errors.Join(result, worker.Shutdown(cleanup))
	}()
	select {
	case err := <-done:
		return err
	case err := <-worker.Errors():
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func main() {
	directory := flag.String("deadletters", ".cache/guide-deadletters", "durable failed-job directory")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := process(ctx, *directory, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
