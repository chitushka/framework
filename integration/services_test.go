package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chitushka/framework/cache"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/migration"
	"github.com/chitushka/framework/queue"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Missing configuration is a failure, never a silent skip. This module is run
// separately from the offline unit tests in the parent module.
func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required; use scripts/integration.sh or scripts/integration.ps1", name)
	}
	return value
}

func unique() string { return fmt.Sprintf("it_%x", randomBytes()) }

func randomBytes() []byte {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return value
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSQL(t *testing.T) {
	for _, backend := range []struct{ name, driver, env string }{
		{db.PostgreSQL, "pgx", "POSTGRES_DSN"},
		{db.MySQL, "mysql", "MYSQL_DSN"},
	} {
		t.Run(backend.name, func(t *testing.T) {
			ctx := testContext(t)
			connection, err := db.Open(ctx, db.Config{
				Driver: backend.driver, DSN: required(t, backend.env), Dialect: backend.name,
				MaxOpenConns: 4, MaxIdleConns: 2, PingTimeout: 5 * time.Second,
			}, db.NewDefaultDialectRegistry())
			check(t, err)
			t.Cleanup(func() { check(t, connection.Close()) })
			q := connection.Dialect().QuoteIdentifier
			drop := func(table string) {
				t.Cleanup(func() {
					cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_, err := connection.ExecContext(cleanup, "DROP TABLE IF EXISTS "+q(table))
					check(t, err)
				})
			}
			table := unique()
			drop(table)
			_, err = connection.ExecContext(ctx, "CREATE TABLE "+q(table)+" (id INTEGER PRIMARY KEY, value VARCHAR(255) NOT NULL)")
			check(t, err)
			insert := "INSERT INTO " + q(table) + " (id, value) VALUES (" + connection.Dialect().Placeholder(1) + ", " + connection.Dialect().Placeholder(2) + ")"
			payload := "quote' OR 1=1 -- Привет"
			rollback := errors.New("rollback savepoint")
			check(t, connection.Transaction(ctx, nil, func(tx *db.Transaction) error {
				if _, err := tx.ExecContext(ctx, insert, 1, payload); err != nil {
					return err
				}
				err := tx.Transaction(ctx, func(child *db.Transaction) error {
					if _, err := child.ExecContext(ctx, insert, 2, "discard"); err != nil {
						return err
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					return fmt.Errorf("nested rollback: %w", err)
				}
				return nil
			}))
			query, args, err := db.NewQueryBuilder(connection.Dialect()).Select("value").From(table).Where(db.Eq("id", 1)).Build()
			check(t, err)
			var value string
			check(t, connection.QueryRowContext(ctx, query, args...).Scan(&value))
			if value != payload {
				t.Fatalf("round trip: got %q, want %q", value, payload)
			}
			var count int
			check(t, connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+q(table)).Scan(&count))
			if count != 1 {
				t.Fatalf("savepoint rollback left %d rows", count)
			}
			check(t, connection.Transaction(ctx, nil, func(tx *db.Transaction) error {
				_, err := tx.ExecContext(ctx, insert, 3, "committed")
				return err
			}))
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := connection.Ping(cancelled); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled ping: %v", err)
			}

			cacheTable := unique()
			drop(cacheTable)
			storage, err := cache.NewDatabaseBackend(cache.DatabaseConfig{DB: connection, Dialect: connection.Dialect(), Table: cacheTable})
			check(t, err)
			_, err = connection.ExecContext(ctx, storage.SchemaSQL())
			check(t, err)
			t.Run("cache", func(t *testing.T) { backendContract(t, storage) })

			queueTable := unique()
			drop(queueTable)
			broker, err := queue.NewDatabaseBroker(queue.DatabaseConfig{DB: connection, Dialect: connection.Dialect(), Table: queueTable})
			check(t, err)
			_, err = connection.ExecContext(ctx, broker.SchemaSQL())
			check(t, err)
			jobs, err := queue.New(broker)
			check(t, err)
			id, err := jobs.Push(ctx, "smoke", map[string]int{"id": 42}, queue.Options{})
			check(t, err)
			delivery, err := broker.Reserve(ctx, time.Second)
			check(t, err)
			if delivery.Message().ID != id {
				t.Fatal("reserved another job")
			}
			check(t, delivery.Ack(ctx))
			if _, err := broker.Reserve(ctx, 0); !errors.Is(err, queue.ErrEmpty) {
				t.Fatalf("acked queue is not empty: %v", err)
			}

			history, migrated := unique(), unique()
			drop(history)
			drop(history + "_state")
			drop(migrated)
			directory := t.TempDir()
			check(t, os.WriteFile(filepath.Join(directory, "20261104000000_smoke.up.sql"), []byte("CREATE TABLE "+q(migrated)+" (id INTEGER PRIMARY KEY)"), 0600))
			check(t, os.WriteFile(filepath.Join(directory, "20261104000000_smoke.down.sql"), []byte("DROP TABLE "+q(migrated)), 0600))
			runner, err := migration.NewRunner(connection, migration.DirectorySource{Path: directory}, migration.Options{Table: history})
			check(t, err)
			applied, err := runner.Up(ctx, 0)
			check(t, err)
			if len(applied) != 1 {
				t.Fatalf("applied %d migrations", len(applied))
			}
			applied, err = runner.Up(ctx, 0)
			check(t, err)
			if len(applied) != 0 {
				t.Fatal("sequential up applied the migration twice")
			}
			reverted, err := runner.Down(ctx, 1)
			check(t, err)
			if len(reverted) != 1 {
				t.Fatal("migration was not reverted")
			}
		})
	}
}

func backendContract(t *testing.T, backend cache.Backend) {
	t.Helper()
	ctx := testContext(t)
	key := unique()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		check(t, backend.Delete(ctx, key))
	})
	_, found, err := backend.Get(ctx, key)
	check(t, err)
	if found {
		t.Fatal("new key already exists")
	}
	value := []byte("binary\x00\r\nПривет")
	check(t, backend.Set(ctx, key, value, 10*time.Second))
	got, found, err := backend.Get(ctx, key)
	check(t, err)
	if !found || !bytes.Equal(got, value) {
		t.Fatalf("round trip: got %q, found %v", got, found)
	}
	check(t, backend.Set(ctx, key, []byte("replacement"), 10*time.Second))
	got, found, err = backend.Get(ctx, key)
	check(t, err)
	if !found || string(got) != "replacement" {
		t.Fatal("overwrite failed")
	}
	check(t, backend.Delete(ctx, key))
	_, found, err = backend.Get(ctx, key)
	check(t, err)
	if found {
		t.Fatal("deleted key still exists")
	}
	check(t, backend.Set(ctx, key, value, time.Second))
	expiry, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, found, err = backend.Get(expiry, key)
		check(t, err)
		if !found {
			break
		}
		select {
		case <-expiry.Done():
			t.Fatal("TTL did not expire")
		case <-ticker.C:
		}
	}
}

func TestRedis(t *testing.T) {
	backend, err := cache.NewRedisBackend(cache.RedisConfig{
		Address: required(t, "REDIS_ADDRESS"), Password: required(t, "REDIS_PASSWORD"),
		Database: 1, DialTimeout: 3 * time.Second, IOTimeout: 3 * time.Second,
	})
	check(t, err)
	backendContract(t, backend)
}

func TestMemcached(t *testing.T) {
	backend, err := cache.NewMemcachedBackend(cache.MemcachedConfig{
		Address: required(t, "MEMCACHED_ADDRESS"), DialTimeout: 3 * time.Second, IOTimeout: 3 * time.Second,
	})
	check(t, err)
	backendContract(t, backend)
}

// Infrastructure smoke only: queue.RabbitChannel has no shipped SDK adapter yet.
// Do not claim this test verifies queue.RabbitMQBroker against a real broker.
func TestRabbitMQInfrastructure(t *testing.T) {
	ctx := testContext(t)
	connection, err := amqp.DialConfig(required(t, "AMQP_URL"), amqp.Config{
		Dial: func(network, address string) (net.Conn, error) {
			socket, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
			if err == nil {
				err = socket.SetDeadline(time.Now().Add(20 * time.Second))
				if err != nil {
					_ = socket.Close()
				}
			}
			return socket, err
		},
	})
	check(t, err)
	t.Cleanup(func() { check(t, connection.Close()) })
	channel, err := connection.Channel()
	check(t, err)
	t.Cleanup(func() { check(t, channel.Close()) })
	declared, err := channel.QueueDeclare("", false, true, true, false, nil)
	check(t, err)
	check(t, channel.Confirm(false))
	confirms := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	body := []byte("infrastructure round trip")
	check(t, channel.PublishWithContext(ctx, "", declared.Name, false, false, amqp.Publishing{Body: body}))
	select {
	case confirm, ok := <-confirms:
		if !ok || !confirm.Ack {
			t.Fatal("publish was not confirmed")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	delivery, ok, err := channel.Get(declared.Name, false)
	check(t, err)
	if !ok || !bytes.Equal(delivery.Body, body) {
		t.Fatal("AMQP round trip failed")
	}
	check(t, delivery.Ack(false))
}
