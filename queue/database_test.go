package queue

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

func TestDatabaseBrokerLifecycleAndLeaseRecovery(t *testing.T) {
	database := openQueueDatabase(t)
	defer func() { _ = database.Close() }()
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	broker, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect, Table: "app.jobs", ReservationTimeout: time.Second, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	broker.now = func() time.Time { return now }
	message := Message{ID: "id-1", Name: "job", Payload: jsonPayload(`{"ok":true}`), MaxAttempts: 3, CreatedAt: now}
	if err := broker.Enqueue(context.Background(), message, 0); err != nil {
		t.Fatal(err)
	}
	first, err := broker.Reserve(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Nack(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	retry, err := broker.Reserve(context.Background(), 0)
	if err != nil || retry.Message().Attempt != 1 {
		t.Fatalf("retry = %#v, %v", retry, err)
	}
	now = now.Add(2 * time.Second)
	recovered, err := broker.Reserve(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.Ack(context.Background()); err == nil {
		t.Fatal("stale reservation acknowledged")
	}
	if err := recovered.Ack(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Reserve(context.Background(), 0); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty = %v", err)
	}
	ddl := broker.SchemaSQL()
	if !strings.Contains(ddl, `CREATE TABLE "app"."jobs"`) || !strings.Contains(ddl, "BYTEA") {
		t.Fatalf("schema = %q", ddl)
	}
}

func TestDatabaseTraceMetadataRequiresSchemaAndSurvivesRetry(t *testing.T) {
	database := openQueueDatabase(t)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	legacy, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect})
	if err != nil {
		t.Fatal(err)
	}
	message := Message{ID: "trace", Name: "job", Payload: jsonPayload(`null`), MaxAttempts: 3, CreatedAt: time.Now(), TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", TraceState: "vendor=value"}
	if err := legacy.Enqueue(t.Context(), message, 0); err == nil {
		t.Fatal("SQL silently dropped trace metadata")
	}
	broker, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect, TraceContext: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(broker.SchemaSQL(), `"traceparent" VARCHAR(512) NULL`) || strings.Contains(legacy.SchemaSQL(), "traceparent") {
		t.Fatal("trace schema opt-in")
	}
	if err := broker.Enqueue(t.Context(), message, 0); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		delivery, err := broker.Reserve(t.Context(), 0)
		if err != nil {
			t.Fatal(err)
		}
		got := delivery.Message()
		if got.TraceParent != message.TraceParent || got.TraceState != message.TraceState || got.Attempt != attempt {
			t.Fatal("lost trace metadata", got)
		}
		if attempt == 0 {
			err = delivery.Nack(t.Context(), 0)
		} else {
			err = delivery.Ack(t.Context())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	message.ID, message.TraceParent, message.TraceState = "old", "", ""
	if err := legacy.Enqueue(t.Context(), message, 0); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Reserve(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.Message().TraceParent != "" {
		t.Fatal("old NULL metadata is not empty")
	}
	if err := delivery.Ack(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseBrokerValidationAndDialects(t *testing.T) {
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	if _, err := NewDatabaseBroker(DatabaseConfig{Dialect: dialect}); err == nil {
		t.Fatal("nil database accepted")
	}
	database := openQueueDatabase(t)
	defer func() { _ = database.Close() }()
	if _, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect, Table: "bad;drop"}); err == nil {
		t.Fatal("unsafe table accepted")
	}
	if _, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect, ReservationTimeout: -1}); err == nil {
		t.Fatal("negative reservation timeout accepted")
	}
	if _, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect, PollInterval: -1}); err == nil {
		t.Fatal("negative polling accepted")
	}
	broker, _ := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect, PollInterval: time.Millisecond})
	if _, err := broker.Reserve(context.Background(), 3*time.Millisecond); !errors.Is(err, ErrEmpty) {
		t.Fatalf("wait empty = %v", err)
	}
	if _, err := broker.Reserve(nil, 0); err == nil { //nolint:staticcheck // SA1012: intentionally tests rejection of a nil context.
		t.Fatal("nil reserve context accepted")
	}
	if _, err := broker.Reserve(context.Background(), -1); err == nil {
		t.Fatal("negative reserve wait accepted")
	}
	if err := broker.Enqueue(context.Background(), Message{}, 0); err == nil {
		t.Fatal("invalid database message accepted")
	}
	valid := Message{ID: "validation", Name: "job", Payload: jsonPayload(`null`), MaxAttempts: 2, CreatedAt: time.Now()}
	if err := broker.Enqueue(nil, valid, 0); err == nil { //nolint:staticcheck // SA1012: intentionally tests rejection of a nil context.
		t.Fatal("nil database context accepted")
	}
	if err := broker.Enqueue(context.Background(), valid, -1); err == nil {
		t.Fatal("negative database delay accepted")
	}
	if err := broker.Enqueue(context.Background(), valid, 0); err != nil {
		t.Fatal(err)
	}
	delivery, err := broker.Reserve(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := delivery.Nack(context.Background(), -1); err == nil {
		t.Fatal("negative database retry accepted")
	}
	for _, name := range []string{frameworkdb.MySQL, frameworkdb.Oracle, frameworkdb.SQLServer} {
		dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(name)
		broker, err := NewDatabaseBroker(DatabaseConfig{DB: database, Dialect: dialect})
		if err != nil || !strings.Contains(broker.SchemaSQL(), "CREATE TABLE") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func jsonPayload(value string) []byte { return []byte(value) }

var queueDriverSequence atomic.Uint64

type queueDBItem struct {
	message     Message
	available   int64
	reservation string
	reserved    int64
}
type queueDBState struct {
	mu    sync.Mutex
	items map[string]*queueDBItem
}
type queueDriver struct{ state *queueDBState }

func openQueueDatabase(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("chitu-queue-%d", queueDriverSequence.Add(1))
	sql.Register(name, queueDriver{&queueDBState{items: map[string]*queueDBItem{}}})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return database
}
func (value queueDriver) Open(string) (driver.Conn, error) {
	return &queueConnection{state: value.state}, nil
}

type queueConnection struct{ state *queueDBState }

func (*queueConnection) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (*queueConnection) Close() error                        { return nil }
func (*queueConnection) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (connection *queueConnection) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	switch {
	case strings.HasPrefix(query, "INSERT INTO"):
		message := Message{ID: args[0].Value.(string), Name: args[1].Value.(string), Payload: append([]byte(nil), args[2].Value.([]byte)...), Attempt: int(args[3].Value.(int64)), MaxAttempts: int(args[4].Value.(int64)), CreatedAt: time.Unix(0, args[6].Value.(int64))}
		if len(args) == 11 {
			message.TraceParent, message.TraceState = args[9].Value.(string), args[10].Value.(string)
		}
		connection.state.items[message.ID] = &queueDBItem{message: message, available: args[5].Value.(int64)}
	case strings.HasPrefix(query, "UPDATE") && strings.Contains(query, "SET \"reservation\""):
		token := args[0].Value.(string)
		reserved := args[1].Value.(int64)
		id := args[2].Value.(string)
		cutoff := args[3].Value.(int64)
		item := connection.state.items[id]
		if item == nil || (item.reservation != "" && item.reserved > cutoff) {
			return driver.RowsAffected(0), nil
		}
		if len(args) > 4 && (int64(item.message.Attempt) != args[4].Value.(int64) || item.available > args[5].Value.(int64)) {
			return driver.RowsAffected(0), nil
		}
		item.reservation = token
		item.reserved = reserved
	case strings.HasPrefix(query, "UPDATE") && strings.Contains(query, "SET \"attempt\""):
		available := args[0].Value.(int64)
		id := args[1].Value.(string)
		token := args[2].Value.(string)
		item := connection.state.items[id]
		if item == nil || item.reservation != token {
			return driver.RowsAffected(0), nil
		}
		item.message.Attempt++
		item.available = available
		item.reservation = ""
		item.reserved = 0
	case strings.HasPrefix(query, "DELETE"):
		id, token := args[0].Value.(string), args[1].Value.(string)
		item := connection.state.items[id]
		if item == nil || item.reservation != token {
			return driver.RowsAffected(0), nil
		}
		delete(connection.state.items, id)
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	return driver.RowsAffected(1), nil
}
func (connection *queueConnection) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	now, cutoff := args[0].Value.(int64), args[1].Value.(int64)
	var selected *queueDBItem
	for _, item := range connection.state.items {
		if item.available <= now && (item.reservation == "" || item.reserved <= cutoff) {
			if selected == nil || item.available < selected.available {
				selected = item
			}
		}
	}
	rows := &queueRows{columns: []string{"id", "name", "payload", "attempt", "max_attempts", "created_at"}}
	tracing := strings.Contains(query, `"traceparent"`)
	if tracing {
		rows.columns = append(rows.columns, "traceparent", "tracestate")
	}
	if selected != nil {
		rows.values = [][]driver.Value{{selected.message.ID, selected.message.Name, []byte(selected.message.Payload), int64(selected.message.Attempt), int64(selected.message.MaxAttempts), selected.message.CreatedAt.UnixNano()}}
		if tracing {
			var parent, state driver.Value
			if selected.message.TraceParent != "" {
				parent = selected.message.TraceParent
			}
			if selected.message.TraceState != "" {
				state = selected.message.TraceState
			}
			rows.values[0] = append(rows.values[0], parent, state)
		}
	}
	return rows, nil
}

type queueRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *queueRows) Columns() []string { return rows.columns }
func (*queueRows) Close() error           { return nil }
func (rows *queueRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var _ driver.Driver = queueDriver{}
var _ driver.ExecerContext = (*queueConnection)(nil)
var _ driver.QueryerContext = (*queueConnection)(nil)
