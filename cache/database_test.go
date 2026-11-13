package cache

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

func TestDatabaseBackendLifecycleAndSchema(t *testing.T) {
	database := openCacheTestDatabase(t)
	defer func() { _ = database.Close() }()
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	backend, err := NewDatabaseBackend(DatabaseConfig{DB: database, Dialect: dialect, Table: "app.cache_items"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := backend.Set(ctx, "key", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	value, found, err := backend.Get(ctx, "key")
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	if err := backend.Set(ctx, "key", []byte("updated"), 0); err != nil {
		t.Fatal(err)
	}
	value, found, err = backend.Get(ctx, "key")
	if err != nil || !found || string(value) != "updated" {
		t.Fatalf("updated Get() = %q, %v, %v", value, found, err)
	}
	if err := backend.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := backend.Get(ctx, "key"); found {
		t.Fatal("deleted database item found")
	}
	backend.now = func() time.Time { return time.Unix(100, 0) }
	if err := backend.Set(ctx, "expired", []byte("old"), time.Second); err != nil {
		t.Fatal(err)
	}
	backend.now = func() time.Time { return time.Unix(102, 0) }
	if _, found, err := backend.Get(ctx, "expired"); err != nil || found {
		t.Fatalf("expired item found=%v err=%v", found, err)
	}
	if got := backend.SchemaSQL(); !strings.Contains(got, `CREATE TABLE "app"."cache_items"`) || !strings.Contains(got, "BYTEA") {
		t.Fatalf("SchemaSQL() = %q", got)
	}
}

func TestDatabaseBackendValidation(t *testing.T) {
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	if _, err := NewDatabaseBackend(DatabaseConfig{Dialect: dialect}); err == nil {
		t.Fatal("nil database accepted")
	}
	database := openCacheTestDatabase(t)
	defer func() { _ = database.Close() }()
	if _, err := NewDatabaseBackend(DatabaseConfig{DB: database, Dialect: dialect, Table: "cache;drop"}); err == nil {
		t.Fatal("unsafe table accepted")
	}
	for _, name := range []string{frameworkdb.MySQL, frameworkdb.Oracle, frameworkdb.SQLServer} {
		dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(name)
		backend, err := NewDatabaseBackend(DatabaseConfig{DB: database, Dialect: dialect})
		if err != nil || !strings.HasPrefix(backend.SchemaSQL(), "CREATE TABLE") {
			t.Fatalf("%s schema: %q, %v", name, backend.SchemaSQL(), err)
		}
	}
}

func TestDBDependencyFingerprintChanges(t *testing.T) {
	database := openCacheTestDatabase(t)
	defer func() { _ = database.Close() }()
	dependency := DBDependency{DB: database, Query: "dependency"}
	first, err := dependency.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), "INSERT dependency", "x"); err != nil {
		t.Fatal(err)
	}
	second, err := dependency.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("fingerprint did not change")
	}
	if _, err := (DBDependency{}).Fingerprint(context.Background()); err == nil {
		t.Fatal("empty DB dependency accepted")
	}
}

var cacheDriverSequence atomic.Uint64

type cacheDBState struct {
	mu      sync.Mutex
	items   map[string]cacheDBItem
	version int64
}

type cacheDBItem struct {
	value   []byte
	expires driver.Value
}

func openCacheTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("chitu-cache-test-%d", cacheDriverSequence.Add(1))
	sql.Register(name, cacheTestDriver{state: &cacheDBState{items: make(map[string]cacheDBItem)}})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return database
}

type cacheTestDriver struct{ state *cacheDBState }

func (testDriver cacheTestDriver) Open(string) (driver.Conn, error) {
	return &cacheTestConnection{state: testDriver.state}, nil
}

type cacheTestConnection struct{ state *cacheDBState }

func (*cacheTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (*cacheTestConnection) Close() error               { return nil }
func (*cacheTestConnection) Begin() (driver.Tx, error)  { return nil, errors.New("not supported") }
func (*cacheTestConnection) Ping(context.Context) error { return nil }

func (connection *cacheTestConnection) ExecContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	switch {
	case strings.HasPrefix(query, "UPDATE"):
		key := arguments[2].Value.(string)
		if _, found := connection.state.items[key]; !found {
			return driver.RowsAffected(0), nil
		}
		connection.state.items[key] = cacheDBItem{value: append([]byte(nil), arguments[0].Value.([]byte)...), expires: arguments[1].Value}
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(query, "INSERT INTO"):
		key := arguments[0].Value.(string)
		connection.state.items[key] = cacheDBItem{value: append([]byte(nil), arguments[1].Value.([]byte)...), expires: arguments[2].Value}
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(query, "DELETE"):
		delete(connection.state.items, arguments[0].Value.(string))
		return driver.RowsAffected(1), nil
	case query == "INSERT dependency":
		connection.state.version++
		return driver.RowsAffected(1), nil
	default:
		return nil, errors.New("unexpected exec")
	}
}

func (connection *cacheTestConnection) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	if query == "dependency" {
		return &cacheTestRows{columns: []string{"version"}, rows: [][]driver.Value{{connection.state.version}}}, nil
	}
	if !strings.HasPrefix(query, `SELECT "value"`) {
		return nil, errors.New("unexpected query")
	}
	item, found := connection.state.items[arguments[0].Value.(string)]
	if !found {
		return &cacheTestRows{columns: []string{"value", "expires_at"}}, nil
	}
	return &cacheTestRows{columns: []string{"value", "expires_at"}, rows: [][]driver.Value{{item.value, item.expires}}}, nil
}

type cacheTestRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (rows *cacheTestRows) Columns() []string { return rows.columns }
func (*cacheTestRows) Close() error           { return nil }
func (rows *cacheTestRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(destination, rows.rows[rows.index])
	rows.index++
	return nil
}

var (
	_ driver.Driver         = cacheTestDriver{}
	_ driver.Conn           = (*cacheTestConnection)(nil)
	_ driver.Pinger         = (*cacheTestConnection)(nil)
	_ driver.ExecerContext  = (*cacheTestConnection)(nil)
	_ driver.QueryerContext = (*cacheTestConnection)(nil)
)
