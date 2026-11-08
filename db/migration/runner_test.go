package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

var migrationDriverSequence atomic.Uint64

func TestRunnerUpStatusAndDown(t *testing.T) {
	connection, recorder := openMigrationConnection(t, frameworkdb.PostgreSQL)
	defer connection.Close()
	now := time.Date(2026, 11, 1, 10, 0, 0, 123, time.UTC)
	source := memorySource{
		{Version: "20261101090000", Name: "users", UpSQL: "UP users", DownSQL: "DOWN users"},
		{Version: "20261101090100", Name: "posts", UpSQL: "UP posts", DownSQL: "DOWN posts"},
	}
	runner, err := NewRunner(connection, source, Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	results, err := runner.Up(context.Background(), 1)
	if err != nil {
		t.Fatalf("Up(1) error = %v", err)
	}
	if len(results) != 1 || results[0].ID != source[0].ID() {
		t.Fatalf("Up(1) = %#v", results)
	}
	statuses, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(statuses) != 2 || statuses[0].State != StateApplied || statuses[0].AppliedAt != now || statuses[1].State != StatePending {
		t.Fatalf("Status() = %#v", statuses)
	}

	results, err = runner.Up(context.Background(), 0)
	if err != nil || len(results) != 1 || results[0].ID != source[1].ID() {
		t.Fatalf("Up(0) = %#v, %v", results, err)
	}
	results, err = runner.Down(context.Background(), 1)
	if err != nil || len(results) != 1 || results[0].ID != source[1].ID() {
		t.Fatalf("Down(1) = %#v, %v", results, err)
	}
	if got := recorder.committedMigrationSQL(); !equalStrings(got, []string{"UP users", "UP posts", "DOWN posts"}) {
		t.Fatalf("committed migration SQL = %#v", got)
	}
}

func TestRunnerDetectsModifiedAndMissingMigrations(t *testing.T) {
	connection, _ := openMigrationConnection(t, frameworkdb.PostgreSQL)
	defer connection.Close()
	original := Migration{Version: "20261101090000", Name: "users", UpSQL: "UP", DownSQL: "DOWN"}
	source := &mutableSource{migrations: []Migration{original}}
	runner, _ := NewRunner(connection, source, Options{})
	if _, err := runner.Up(context.Background(), 0); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	source.migrations[0].UpSQL = "CHANGED"
	statuses, err := runner.Status(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].State != StateModified {
		t.Fatalf("modified Status() = %#v, %v", statuses, err)
	}
	if _, err := runner.Up(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "was modified") {
		t.Fatalf("modified Up() error = %v", err)
	}

	source.migrations = nil
	statuses, err = runner.Status(context.Background())
	if err != nil || len(statuses) != 1 || statuses[0].State != StateMissing {
		t.Fatalf("missing Status() = %#v, %v", statuses, err)
	}
	if _, err := runner.Down(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "missing from source") {
		t.Fatalf("missing Down() error = %v", err)
	}
}

func TestRunnerRollsBackFailedMigration(t *testing.T) {
	connection, recorder := openMigrationConnection(t, frameworkdb.PostgreSQL)
	defer connection.Close()
	recorder.failQuery = "BROKEN"
	runner, _ := NewRunner(connection, memorySource{{
		Version: "20261101090000", Name: "broken", UpSQL: "BROKEN", DownSQL: "DOWN",
	}}, Options{})
	if _, err := runner.Up(context.Background(), 0); err == nil {
		t.Fatal("Up() error = nil")
	}
	if len(recorder.historySnapshot()) != 0 || recorder.rollbacks != 1 {
		t.Fatalf("history = %#v, rollbacks = %d", recorder.historySnapshot(), recorder.rollbacks)
	}
}

func TestRunnerUsesDialectHistoryDDL(t *testing.T) {
	tests := []struct {
		dialect string
		part    string
	}{
		{frameworkdb.PostgreSQL, "CREATE TABLE IF NOT EXISTS \"public\".\"chitu_migration\""},
		{frameworkdb.MySQL, "CREATE TABLE IF NOT EXISTS `testdb`.`chitu_migration`"},
	}
	for _, tt := range tests {
		t.Run(tt.dialect, func(t *testing.T) {
			connection, recorder := openMigrationConnection(t, tt.dialect)
			defer connection.Close()
			runner, _ := NewRunner(connection, memorySource{}, Options{})
			if _, err := runner.Status(context.Background()); err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if len(recorder.executed) == 0 || !strings.Contains(recorder.executed[0], tt.part) {
				t.Fatalf("history DDL = %#v, want part %q", recorder.executed, tt.part)
			}
		})
	}
}

type memorySource []Migration

func (source memorySource) Migrations(context.Context) ([]Migration, error) {
	return append([]Migration(nil), source...), nil
}

type mutableSource struct{ migrations []Migration }

func (source *mutableSource) Migrations(context.Context) ([]Migration, error) {
	return append([]Migration(nil), source.migrations...), nil
}

func openMigrationConnection(t *testing.T, dialect string) (*frameworkdb.Connection, *migrationRecorder) {
	t.Helper()
	recorder := &migrationRecorder{history: make(map[string]historyRecord)}
	connection, err := migrationTestOpen(t, dialect, recorder)(context.Background(), frameworkdb.Config{})
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	return connection, recorder
}

func migrationTestOpen(t *testing.T, dialect string, recorder *migrationRecorder) OpenFunc {
	t.Helper()
	driverName := fmt.Sprintf("chitu-migration-test-%d", migrationDriverSequence.Add(1))
	sql.Register(driverName, migrationDriver{recorder: recorder})
	return func(ctx context.Context, _ frameworkdb.Config) (*frameworkdb.Connection, error) {
		return frameworkdb.Open(ctx, frameworkdb.Config{
			Driver: driverName, Dialect: dialect,
		}, frameworkdb.NewDefaultDialectRegistry())
	}
}

type historyRecord struct {
	checksum  string
	appliedAt string
}

type migrationRecorder struct {
	mu          sync.Mutex
	history     map[string]historyRecord
	executed    []string
	committed   []string
	failQuery   string
	rollbacks   int
	dirty       map[string]dirtyTestRecord
	owner       *migrationDriverConn
	busy        bool
	failRelease bool
}

type dirtyTestRecord struct{ checksum, direction, started string }

func (r *migrationRecorder) historySnapshot() map[string]historyRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]historyRecord, len(r.history))
	for key, value := range r.history {
		result[key] = value
	}
	return result
}

func (r *migrationRecorder) committedMigrationSQL() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.committed...)
}

type migrationDriver struct{ recorder *migrationRecorder }

func (d migrationDriver) Open(string) (driver.Conn, error) {
	return &migrationDriverConn{recorder: d.recorder}, nil
}

type migrationDriverConn struct {
	recorder *migrationRecorder
	tx       *migrationDriverTx
}

func (*migrationDriverConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not supported")
}
func (c *migrationDriverConn) Close() error {
	c.recorder.mu.Lock()
	defer c.recorder.mu.Unlock()
	if c.recorder.owner == c {
		c.recorder.owner = nil
	}
	return nil
}
func (connection *migrationDriverConn) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}
func (*migrationDriverConn) Ping(context.Context) error { return nil }

func (connection *migrationDriverConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	tx := &migrationDriverTx{connection: connection}
	connection.tx = tx
	return tx, nil
}

func (connection *migrationDriverConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	connection.recorder.mu.Lock()
	defer connection.recorder.mu.Unlock()
	connection.recorder.executed = append(connection.recorder.executed, query)
	if query == connection.recorder.failQuery {
		return nil, errors.New("forced migration failure")
	}
	op := recordedOperation{query: query, args: namedValues(args)}
	if connection.tx != nil {
		connection.tx.operations = append(connection.tx.operations, op)
	} else {
		applyOperation(connection.recorder, op)
	}
	return driver.RowsAffected(1), nil
}

func (connection *migrationDriverConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	connection.recorder.mu.Lock()
	defer connection.recorder.mu.Unlock()
	r := connection.recorder
	valueRow := func(value driver.Value) (driver.Rows, error) {
		return &migrationRows{columns: []string{"value"}, rows: [][]driver.Value{{value}}}, nil
	}
	switch {
	case strings.Contains(query, "pg_try_advisory_lock"), strings.Contains(query, "GET_LOCK"):
		acquired := !r.busy && (r.owner == nil || r.owner == connection)
		if acquired {
			r.owner = connection
		}
		if strings.Contains(query, "GET_LOCK") {
			if acquired {
				return valueRow(int64(1))
			}
			return valueRow(int64(0))
		}
		return valueRow(acquired)
	case strings.Contains(query, "pg_advisory_unlock"), strings.Contains(query, "RELEASE_LOCK"):
		if r.failRelease {
			return nil, errors.New("forced release failure")
		}
		owned := r.owner == connection
		if owned {
			r.owner = nil
		}
		if strings.Contains(query, "RELEASE_LOCK") {
			if owned {
				return valueRow(int64(1))
			}
			return valueRow(int64(0))
		}
		return valueRow(owned)
	case query == "SELECT DATABASE()":
		return valueRow("testdb")
	case strings.HasPrefix(query, "SELECT COALESCE"):
		return valueRow("public")
	case strings.HasPrefix(query, "SELECT ENGINE"):
		return valueRow("InnoDB")
	case strings.Contains(query, "started_at"):
		var rows [][]driver.Value
		for id, entry := range r.dirty {
			rows = append(rows, []driver.Value{id, entry.checksum, entry.direction, entry.started})
		}
		return &migrationRows{columns: []string{"version", "checksum", "direction", "started_at"}, rows: rows}, nil
	}
	ids := make([]string, 0, len(connection.recorder.history))
	for id := range connection.recorder.history {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([][]driver.Value, len(ids))
	for i, id := range ids {
		record := connection.recorder.history[id]
		rows[i] = []driver.Value{id, record.checksum, record.appliedAt}
	}
	return &migrationRows{rows: rows}, nil
}

type recordedOperation struct {
	query string
	args  []driver.Value
}

type migrationDriverTx struct {
	connection *migrationDriverConn
	operations []recordedOperation
}

func (tx *migrationDriverTx) Commit() error {
	tx.connection.recorder.mu.Lock()
	defer tx.connection.recorder.mu.Unlock()
	for _, operation := range tx.operations {
		applyOperation(tx.connection.recorder, operation)
	}
	tx.connection.tx = nil
	return nil
}

func (tx *migrationDriverTx) Rollback() error {
	tx.connection.recorder.mu.Lock()
	defer tx.connection.recorder.mu.Unlock()
	tx.connection.recorder.rollbacks++
	tx.connection.tx = nil
	return nil
}

func applyOperation(recorder *migrationRecorder, operation recordedOperation) {
	if strings.Contains(operation.query, "_state") {
		if recorder.dirty == nil {
			recorder.dirty = make(map[string]dirtyTestRecord)
		}
		switch {
		case strings.HasPrefix(operation.query, "INSERT INTO"):
			recorder.dirty[operation.args[0].(string)] = dirtyTestRecord{checksum: operation.args[1].(string), direction: operation.args[2].(string), started: operation.args[3].(string)}
		case strings.HasPrefix(operation.query, "DELETE FROM"):
			delete(recorder.dirty, operation.args[0].(string))
		}
		return
	}
	switch {
	case strings.HasPrefix(operation.query, "INSERT INTO"):
		recorder.history[operation.args[0].(string)] = historyRecord{
			checksum: operation.args[1].(string), appliedAt: operation.args[2].(string),
		}
	case strings.HasPrefix(operation.query, "DELETE FROM"):
		delete(recorder.history, operation.args[0].(string))
	case strings.HasPrefix(operation.query, "CREATE TABLE"),
		strings.HasPrefix(operation.query, "IF OBJECT_ID"),
		strings.HasPrefix(operation.query, "BEGIN EXECUTE IMMEDIATE"):
	default:
		recorder.committed = append(recorder.committed, operation.query)
	}
}

func namedValues(values []driver.NamedValue) []driver.Value {
	result := make([]driver.Value, len(values))
	for i, value := range values {
		result[i] = value.Value
	}
	return result
}

type migrationRows struct {
	rows    [][]driver.Value
	index   int
	columns []string
}

func (r *migrationRows) Columns() []string {
	if r.columns != nil {
		return r.columns
	}
	return []string{"version", "checksum", "applied_at"}
}
func (*migrationRows) Close() error { return nil }
func (rows *migrationRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.rows) {
		return io.EOF
	}
	copy(destination, rows.rows[rows.index])
	rows.index++
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

var (
	_ driver.Driver         = migrationDriver{}
	_ driver.Conn           = (*migrationDriverConn)(nil)
	_ driver.ConnBeginTx    = (*migrationDriverConn)(nil)
	_ driver.Pinger         = (*migrationDriverConn)(nil)
	_ driver.ExecerContext  = (*migrationDriverConn)(nil)
	_ driver.QueryerContext = (*migrationDriverConn)(nil)
)
