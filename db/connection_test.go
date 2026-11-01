package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
)

const testDriverName = "chitu-db-test"

var (
	errOpen = errors.New("open failure")
	errPing = errors.New("ping failure")
	closed  atomic.Int64
)

func init() {
	sql.Register(testDriverName, fakeDriver{})
}

func TestConfigValidation(t *testing.T) {
	valid := db.Config{Driver: testDriverName, Dialect: db.PostgreSQL}
	tests := map[string]db.Config{
		"missing driver":        {Dialect: db.PostgreSQL},
		"missing dialect":       {Driver: testDriverName},
		"negative open":         {Driver: testDriverName, Dialect: db.PostgreSQL, MaxOpenConns: -1},
		"negative idle":         {Driver: testDriverName, Dialect: db.PostgreSQL, MaxIdleConns: -1},
		"idle exceeds open":     {Driver: testDriverName, Dialect: db.PostgreSQL, MaxOpenConns: 1, MaxIdleConns: 2},
		"negative lifetime":     {Driver: testDriverName, Dialect: db.PostgreSQL, ConnMaxLifetime: -time.Second},
		"negative idle time":    {Driver: testDriverName, Dialect: db.PostgreSQL, ConnMaxIdleTime: -time.Second},
		"negative ping timeout": {Driver: testDriverName, Dialect: db.PostgreSQL, PingTimeout: -time.Second},
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, config := range tests {
		if err := config.Validate(); err == nil {
			t.Fatalf("%s: expected validation error", name)
		}
	}
}

func TestOpenConfiguresPoolAndDelegatesQueries(t *testing.T) {
	config := db.Config{
		Driver:          testDriverName,
		DSN:             "success",
		Dialect:         db.PostgreSQL,
		MaxOpenConns:    4,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
		ConnMaxIdleTime: 30 * time.Second,
		PingTimeout:     time.Second,
	}
	connection, err := db.Open(context.Background(), config, db.NewDefaultDialectRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	if connection.DB() == nil || connection.Dialect().Name() != db.PostgreSQL || connection.Config() != config {
		t.Fatalf("unexpected connection metadata: %#v", connection.Config())
	}
	if connection.Stats().MaxOpenConnections != config.MaxOpenConns {
		t.Fatalf("MaxOpenConns was not applied: %#v", connection.Stats())
	}
	if err := connection.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := connection.ExecContext(context.Background(), "UPDATE users SET active = ?", true)
	if err != nil {
		t.Fatal(err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		t.Fatalf("unexpected affected rows: %d", affected)
	}
	var value string
	if err := connection.QueryRowContext(context.Background(), "SELECT value").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "result" {
		t.Fatalf("unexpected query value: %q", value)
	}
	statement, err := connection.PrepareContext(context.Background(), "UPDATE prepared")
	if err != nil {
		t.Fatal(err)
	}
	defer statement.Close()
	if _, err := statement.ExecContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := connection.QueryContext(context.Background(), "SELECT rows")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected query row")
	}
}

func TestOpenErrorsAndPingTimeout(t *testing.T) {
	registry := db.NewDefaultDialectRegistry()
	for name, test := range map[string]struct {
		ctx      context.Context
		config   db.Config
		registry *db.DialectRegistry
		want     error
	}{
		"nil context": {
			config:   db.Config{Driver: testDriverName, Dialect: db.PostgreSQL},
			registry: registry,
		},
		"nil registry": {
			ctx:    context.Background(),
			config: db.Config{Driver: testDriverName, Dialect: db.PostgreSQL},
		},
		"unknown dialect": {
			ctx:      context.Background(),
			config:   db.Config{Driver: testDriverName, Dialect: "missing"},
			registry: registry,
		},
		"unknown driver": {
			ctx:      context.Background(),
			config:   db.Config{Driver: "missing-driver", Dialect: db.PostgreSQL},
			registry: registry,
		},
		"open failure": {
			ctx:      context.Background(),
			config:   db.Config{Driver: testDriverName, DSN: "open-error", Dialect: db.PostgreSQL},
			registry: registry,
			want:     errOpen,
		},
		"ping failure": {
			ctx:      context.Background(),
			config:   db.Config{Driver: testDriverName, DSN: "ping-error", Dialect: db.PostgreSQL},
			registry: registry,
			want:     errPing,
		},
		"ping timeout": {
			ctx:      context.Background(),
			config:   db.Config{Driver: testDriverName, DSN: "block", Dialect: db.PostgreSQL, PingTimeout: 10 * time.Millisecond},
			registry: registry,
			want:     context.DeadlineExceeded,
		},
	} {
		t.Run(name, func(t *testing.T) {
			connection, err := db.Open(test.ctx, test.config, test.registry)
			if connection != nil || err == nil {
				t.Fatalf("expected open error, got connection=%v err=%v", connection, err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error %v does not preserve %v", err, test.want)
			}
		})
	}
}

func TestPingFailureClosesOpenedDriverConnection(t *testing.T) {
	before := closed.Load()
	_, err := db.Open(context.Background(), db.Config{
		Driver:  testDriverName,
		DSN:     "ping-error",
		Dialect: db.PostgreSQL,
	}, db.NewDefaultDialectRegistry())
	if !errors.Is(err, errPing) {
		t.Fatal(err)
	}
	if closed.Load() <= before {
		t.Fatal("driver connection was not closed after ping failure")
	}
}

type fakeDriver struct{}

func (fakeDriver) Open(name string) (driver.Conn, error) {
	if name == "open-error" {
		return nil, errOpen
	}
	return &fakeConnection{dsn: name}, nil
}

type fakeConnection struct {
	dsn string
}

func (connection *fakeConnection) Prepare(string) (driver.Stmt, error) {
	return fakeStatement{}, nil
}

func (connection *fakeConnection) Close() error {
	closed.Add(1)
	return nil
}

func (*fakeConnection) Begin() (driver.Tx, error) { return fakeTransaction{}, nil }

func (connection *fakeConnection) Ping(ctx context.Context) error {
	switch connection.dsn {
	case "ping-error":
		return errPing
	case "block":
		<-ctx.Done()
		return ctx.Err()
	default:
		return nil
	}
}

func (*fakeConnection) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (*fakeConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{values: [][]driver.Value{{"result"}}}, nil
}

type fakeStatement struct{}

func (fakeStatement) Close() error  { return nil }
func (fakeStatement) NumInput() int { return -1 }
func (fakeStatement) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (fakeStatement) Query([]driver.Value) (driver.Rows, error) {
	return &fakeRows{values: [][]driver.Value{{"result"}}}, nil
}

type fakeTransaction struct{}

func (fakeTransaction) Commit() error   { return nil }
func (fakeTransaction) Rollback() error { return nil }

type fakeRows struct {
	values [][]driver.Value
	index  int
}

func (*fakeRows) Columns() []string { return []string{"value"} }
func (*fakeRows) Close() error      { return nil }
func (rows *fakeRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var (
	_ driver.Driver         = fakeDriver{}
	_ driver.Conn           = (*fakeConnection)(nil)
	_ driver.Pinger         = (*fakeConnection)(nil)
	_ driver.ExecerContext  = (*fakeConnection)(nil)
	_ driver.QueryerContext = (*fakeConnection)(nil)
)

func TestConfigTags(t *testing.T) {
	typeOf := reflect.TypeFor[db.Config]()
	for _, fieldName := range []string{"Driver", "DSN", "Dialect", "MaxOpenConns", "MaxIdleConns", "ConnMaxLifetime"} {
		field, exists := typeOf.FieldByName(fieldName)
		if !exists || field.Tag.Get("json") == "" || field.Tag.Get("env") == "" {
			t.Fatalf("field %s is missing config tags", fieldName)
		}
	}
}
