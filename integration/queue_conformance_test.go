package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	rabbitadapter "github.com/chitushka/framework/adapters/rabbitmq"
	redisadapter "github.com/chitushka/framework/adapters/redis"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/queue"
	amqp "github.com/rabbitmq/amqp091-go"
	sdk "github.com/redis/go-redis/v9"
)

type gatedSQL struct {
	database *sql.DB
	selected chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (g *gatedSQL) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return g.database.QueryRowContext(ctx, q, args...)
}
func (g *gatedSQL) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(q, "UPDATE") && strings.Contains(q, "reservation") && len(args) >= 4 {
		g.once.Do(func() {
			close(g.selected)
			select {
			case <-g.release:
			case <-ctx.Done():
			}
		})
	}
	return g.database.ExecContext(ctx, q, args...)
}

func TestSQLQueueClaimFencesRetrySnapshot(t *testing.T) {
	sqlBaseline(t, func(t *testing.T, c *db.Connection) {
		ctx := testContext(t)
		name := unique()
		config := queue.DatabaseConfig{DB: c.DB(), Dialect: c.Dialect(), Table: name, ReservationTimeout: time.Second, PollInterval: time.Millisecond}
		a, err := queue.NewDatabaseBroker(config)
		check(t, err)
		_, err = c.ExecContext(ctx, a.SchemaSQL())
		check(t, err)
		t.Cleanup(func() { _, _ = c.ExecContext(context.Background(), "DROP TABLE "+c.Dialect().QuoteIdentifier(name)) })
		message := queue.Message{ID: unique(), Name: "job", Payload: json.RawMessage(`null`), MaxAttempts: 3, CreatedAt: time.Now()}
		check(t, a.Enqueue(ctx, message, 0))
		gate := &gatedSQL{database: c.DB(), selected: make(chan struct{}), release: make(chan struct{})}
		config.DB = gate
		b, err := queue.NewDatabaseBroker(config)
		check(t, err)
		result := make(chan error, 1)
		go func() { _, err := b.Reserve(ctx, 0); result <- err }()
		select {
		case <-gate.selected:
		case <-ctx.Done():
			t.Fatal("second consumer did not select")
		}
		first, err := a.Reserve(ctx, 0)
		check(t, err)
		check(t, first.Nack(ctx, 100*time.Millisecond))
		close(gate.release)
		if err := <-result; !errors.Is(err, queue.ErrEmpty) {
			t.Fatalf("stale consumer claimed delayed envelope: %v", err)
		}
		retry, err := b.Reserve(ctx, time.Second)
		check(t, err)
		if retry.Message().Attempt != 1 {
			t.Fatalf("attempt=%d", retry.Message().Attempt)
		}
		check(t, retry.Ack(ctx))
	})
}

func queueLifecycle(t *testing.T, broker queue.Broker) {
	t.Helper()
	ctx := testContext(t)
	message := queue.Message{ID: unique(), Name: "job", Payload: json.RawMessage(`{"value":1}`), MaxAttempts: 3, CreatedAt: time.Now()}
	check(t, broker.Enqueue(ctx, message, 0))
	delivery, err := broker.Reserve(ctx, time.Second)
	check(t, err)
	nackedAt := time.Now()
	check(t, delivery.Nack(ctx, 300*time.Millisecond))
	retry, err := broker.Reserve(ctx, 0)
	if err == nil && time.Since(nackedAt) < 300*time.Millisecond {
		t.Fatal("retry arrived before requested delay")
	}
	if errors.Is(err, queue.ErrEmpty) {
		retry, err = broker.Reserve(ctx, 3*time.Second)
	}
	check(t, err)
	if retry.Message().Attempt != 1 {
		t.Fatalf("retry attempt=%d", retry.Message().Attempt)
	}
	check(t, retry.Ack(ctx))
	if _, err := broker.Reserve(ctx, 0); !errors.Is(err, queue.ErrEmpty) {
		t.Fatalf("settled message remains: %v", err)
	}
}

func TestRedisQueueLeaseReclaimAndAtomicRetry(t *testing.T) {
	client := redisadapter.NewClient(&sdk.Options{Addr: required(t, "REDIS_ADDRESS"), Password: required(t, "REDIS_PASSWORD"), DB: 1})
	t.Cleanup(func() { check(t, client.Close()) })
	store, err := redisadapter.New(client, unique())
	check(t, err)
	name := unique()
	t.Cleanup(func() { check(t, store.Purge(context.Background(), name)) })
	broker, err := queue.NewRedisBroker(queue.RedisConfig{Store: store, Queue: name, ReservationTimeout: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond})
	check(t, err)
	queueLifecycle(t, broker)
	producer, err := queue.New(broker)
	check(t, err)
	_, err = producer.Push(testContext(t), "job", nil, queue.Options{})
	check(t, err)
	old, err := broker.Reserve(testContext(t), 0)
	check(t, err)
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	reclaimed, err := broker.Reserve(testContext(t), time.Second)
	check(t, err)
	if err := old.Ack(testContext(t)); !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("stale owner acknowledged reclaimed job: %v", err)
	}
	check(t, reclaimed.Ack(testContext(t)))
}

func cleanupRabbitQueue(t *testing.T, name string) {
	t.Helper()
	connection, err := amqp.Dial(required(t, "AMQP_URL"))
	check(t, err)
	defer func() { _ = connection.Close() }()
	ch, err := connection.Channel()
	check(t, err)
	defer func() { _ = ch.Close() }()
	for _, suffix := range []string{"", ".delay", ".invalid"} {
		_, err := ch.QueueDelete(name+suffix, false, false, false)
		check(t, err)
	}
}

func TestRabbitPublisherConfirmsRetryAndDisconnect(t *testing.T) {
	config := rabbitadapter.Config{URL: required(t, "AMQP_URL"), OperationTimeout: 2 * time.Second}
	adapter, err := rabbitadapter.New(config)
	check(t, err)
	t.Cleanup(func() { check(t, adapter.Close()) })
	name := unique()
	t.Cleanup(func() { cleanupRabbitQueue(t, name) })
	broker, err := queue.NewRabbitMQBroker(queue.RabbitMQConfig{Channel: adapter, Queue: name})
	check(t, err)
	queueLifecycle(t, broker)
	producer, err := queue.New(broker)
	check(t, err)
	id, err := producer.Push(testContext(t), "job", nil, queue.Options{})
	check(t, err)
	old, err := broker.Reserve(testContext(t), time.Second)
	check(t, err)
	check(t, adapter.Close()) // closing the consumer connection returns the unacked job
	other, err := rabbitadapter.New(config)
	check(t, err)
	defer func() { _ = other.Close() }()
	replacement, err := queue.NewRabbitMQBroker(queue.RabbitMQConfig{Channel: other, Queue: name})
	check(t, err)
	recovered, err := replacement.Reserve(testContext(t), time.Second)
	check(t, err)
	if recovered.Message().ID != id {
		t.Fatal("disconnect lost accepted message")
	}
	if err := old.Ack(testContext(t)); err == nil {
		t.Fatal("closed consumer acknowledged message")
	}
	check(t, recovered.Ack(testContext(t)))
}

// Executed as a separate test process so process death cannot share locks/state.
func TestQueueProcessHelper(t *testing.T) {
	mode := os.Getenv("CHITU_QUEUE_MODE")
	if mode == "" {
		return
	}
	if os.Getenv("CHITU_QUEUE_KIND") != "" {
		persistentQueueChild(t, mode)
		return
	}
	c, err := db.Open(testContext(t), db.Config{Driver: required(t, "CHITU_QUEUE_DRIVER"), Dialect: required(t, "CHITU_QUEUE_DIALECT"), DSN: required(t, "CHITU_QUEUE_DSN")}, db.NewDefaultDialectRegistry())
	check(t, err)
	defer func() { _ = c.Close() }()
	broker, err := queue.NewDatabaseBroker(queue.DatabaseConfig{DB: c.DB(), Dialect: c.Dialect(), Table: required(t, "CHITU_QUEUE_TABLE"), ReservationTimeout: 200 * time.Millisecond, PollInterval: time.Millisecond})
	check(t, err)
	for {
		delivery, err := broker.Reserve(testContext(t), 20*time.Millisecond)
		if errors.Is(err, queue.ErrEmpty) {
			return
		}
		check(t, err)
		if mode == "die" {
			check(t, os.WriteFile(required(t, "CHITU_QUEUE_READY"), []byte(delivery.Message().ID), 0o600))
			select {}
		}
		check(t, delivery.Ack(testContext(t)))
	}
}

func TestSQLQueueMultipleProcessesAndConsumerDeath(t *testing.T) {
	sqlBaseline(t, func(t *testing.T, c *db.Connection) {
		ctx := testContext(t)
		name := unique()
		broker, err := queue.NewDatabaseBroker(queue.DatabaseConfig{DB: c.DB(), Dialect: c.Dialect(), Table: name, ReservationTimeout: 200 * time.Millisecond, PollInterval: time.Millisecond})
		check(t, err)
		_, err = c.ExecContext(ctx, broker.SchemaSQL())
		check(t, err)
		t.Cleanup(func() { _, _ = c.ExecContext(context.Background(), "DROP TABLE "+c.Dialect().QuoteIdentifier(name)) })
		producer, err := queue.New(broker)
		check(t, err)
		for i := 0; i < 20; i++ {
			_, err := producer.Push(ctx, "job", nil, queue.Options{})
			check(t, err)
		}
		child := func(mode string) *exec.Cmd {
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQueueProcessHelper$")
			command.Env = append(os.Environ(), "CHITU_QUEUE_MODE="+mode, "CHITU_QUEUE_DRIVER="+c.Config().Driver, "CHITU_QUEUE_DIALECT="+c.Dialect().Name(), "CHITU_QUEUE_DSN="+c.Config().DSN, "CHITU_QUEUE_TABLE="+name)
			return command
		}
		var group sync.WaitGroup
		for i := 0; i < 3; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				if out, err := child("consume").CombinedOutput(); err != nil {
					t.Errorf("child: %v\n%s", err, out)
				}
			}()
		}
		group.Wait()
		if _, err := broker.Reserve(ctx, 0); !errors.Is(err, queue.ErrEmpty) {
			t.Fatal("multi-process drain incomplete", err)
		}
		id, err := producer.Push(ctx, "job", nil, queue.Options{})
		check(t, err)
		ready := filepath.Join(t.TempDir(), "reserved")
		command := child("die")
		command.Env = append(command.Env, "CHITU_QUEUE_READY="+ready)
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
				t.Fatal("child did not reserve")
			case <-time.After(10 * time.Millisecond):
			}
		}
		check(t, command.Process.Kill())
		_ = command.Wait()
		recovered, err := broker.Reserve(ctx, time.Second)
		check(t, err)
		if recovered.Message().ID != id {
			t.Fatal("dead consumer lost message")
		}
		check(t, recovered.Ack(ctx))
	})
}
