# Запуск и восстановление worker

## Одна задача без внешнего сервиса

```bash
go run ./examples/guide/jobs
go test ./examples/guide/jobs
```

Ожидается `processed monthly-report`, затем процесс завершается. MemoryBroker
здесь учебный: accepted jobs не переживают restart. FileDeadLetters открыт до
shutdown worker; по умолчанию каталог `.cache/guide-deadletters`. В deployment
выберите persistent volume и права/retention, отдельные от container layer.

```go file=../../../examples/guide/jobs/main.go
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
```

## Замена MemoryBroker и идемпотентность

Для Redis/RabbitMQ используйте отдельный module adapters, для SQL — DatabaseBroker.
Конкретная конфигурация TLS/auth/deadlines, publisher confirms, quarantine и leases —
в [queue contract](../queues.md). Согласуйте lease с JobTimeout + SettlementTimeout
и запасом; automatic lease renewal не поставляется.

Обработчик получает `OperationID` из payload. Для бизнес-операции с SQL сохраните
UNIQUE operation ID в той же транзакции, что mutation. При повторе уже применённой
операции верните success без второго side effect. Для SQL → broker используйте
transactional outbox: сначала commit бизнес-изменения и outbox, затем dispatcher
публикует с confirmation. Crash после publish до delivered допускает дубликат;
process-local map не заменяет durable deduplication.

## Финальная ошибка и replay

Финальная ошибка сохраняется в DeadLetters до Ack. При отказе store worker
останавливается и оставляет доставку для восстановления; наблюдайте Worker.Errors
либо запускайте его как app.Module. Не скрывайте fatal error бесконечным retry loop.
Оператор читает List/Get, исправляет причину, затем вызывает Replay с broker и
сохраняет исходную audit record. Replay создаёт новый ID/Attempt=0; неопределённый
результат enqueue может создать дубликат. Удаление records — отдельное действие
после разбора, а не автоматический побочный эффект replay.

SIGTERM должен прекратить reserve, дать текущим handlers DrainTimeout, затем
cancel и bounded settlement. Cleanup context не наследует уже отменённый request
context. [Worker tests](../../../queue/deadletter_test.go) и
[kill/restart suite](../../../integration/worker_crash_test.go) проверяют эти границы;
tmpfs/process tests не доказывают fsync/host durability.
