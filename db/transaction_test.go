package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chitushka/framework/db"
)

const transactionDriverName = "chitu-transaction-test"

var (
	transactionRecorders sync.Map
	transactionID        atomic.Uint64
	errTransactionSQL    = errors.New("transaction SQL failure")
	errCommit            = errors.New("commit failure")
	errRollback          = errors.New("rollback failure")
)

func init() {
	sql.Register(transactionDriverName, transactionDriver{})
}

func TestRootTransactionLifecycle(t *testing.T) {
	connection, recorder := openTransactionConnection(t, db.PostgreSQL, nil)
	options := &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}
	transaction, err := connection.BeginTx(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.Depth() != 0 || !transaction.Active() || transaction.Dialect().Name() != db.PostgreSQL {
		t.Fatalf("unexpected root transaction state: depth=%d active=%v", transaction.Depth(), transaction.Active())
	}
	if _, err := transaction.ExecContext(context.Background(), "UPDATE root"); err != nil {
		t.Fatal(err)
	}
	rows, err := transaction.QueryContext(context.Background(), "SELECT root")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	statement, err := transaction.PrepareContext(context.Background(), "UPDATE prepared")
	if err != nil {
		t.Fatal(err)
	}
	_ = statement.Close()
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if transaction.Active() {
		t.Fatal("committed transaction is active")
	}
	if err := transaction.Commit(); !errors.Is(err, db.ErrTransactionDone) {
		t.Fatalf("expected transaction done, got %v", err)
	}
	if _, err := transaction.ExecContext(context.Background(), "UPDATE after"); !errors.Is(err, db.ErrTransactionDone) {
		t.Fatalf("expected transaction done from ExecContext, got %v", err)
	}

	statements, recordedOptions := recorder.snapshot()
	if want := []string{"BEGIN", "UPDATE root", "SELECT root", "PREPARE UPDATE prepared", "COMMIT"}; !reflect.DeepEqual(statements, want) {
		t.Fatalf("unexpected transaction statements: %v", statements)
	}
	if recordedOptions.Isolation != driver.IsolationLevel(sql.LevelSerializable) || !recordedOptions.ReadOnly {
		t.Fatalf("transaction options were not passed: %#v", recordedOptions)
	}
}

func TestNestedTransactionsUseDialectSavepoints(t *testing.T) {
	tests := []struct {
		dialect string
		want    []string
	}{
		{
			dialect: db.PostgreSQL,
			want: []string{
				"BEGIN", "SAVEPOINT chitu_sp_1", "UPDATE child", "RELEASE SAVEPOINT chitu_sp_1",
				"UPDATE parent", "SAVEPOINT chitu_sp_2", "ROLLBACK TO SAVEPOINT chitu_sp_2",
				"RELEASE SAVEPOINT chitu_sp_2", "COMMIT",
			},
		},
		{
			dialect: db.MySQL,
			want: []string{
				"BEGIN", "SAVEPOINT chitu_sp_1", "UPDATE child", "RELEASE SAVEPOINT chitu_sp_1",
				"UPDATE parent", "SAVEPOINT chitu_sp_2", "ROLLBACK TO SAVEPOINT chitu_sp_2",
				"RELEASE SAVEPOINT chitu_sp_2", "COMMIT",
			},
		},
		{
			dialect: db.Oracle,
			want: []string{
				"BEGIN", "SAVEPOINT chitu_sp_1", "UPDATE child", "UPDATE parent",
				"SAVEPOINT chitu_sp_2", "ROLLBACK TO chitu_sp_2", "COMMIT",
			},
		},
		{
			dialect: db.SQLServer,
			want: []string{
				"BEGIN", "SAVE TRANSACTION chitu_sp_1", "UPDATE child", "UPDATE parent",
				"SAVE TRANSACTION chitu_sp_2", "ROLLBACK TRANSACTION chitu_sp_2", "COMMIT",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.dialect, func(t *testing.T) {
			connection, recorder := openTransactionConnection(t, test.dialect, nil)
			parent, err := connection.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			child, err := parent.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if child.Depth() != 1 || !child.Active() || parent.Active() {
				t.Fatalf("unexpected nested state: child=%v parent=%v", child.Active(), parent.Active())
			}
			if _, err := parent.ExecContext(context.Background(), "UPDATE blocked"); !errors.Is(err, db.ErrNestedTransactionActive) {
				t.Fatalf("parent operation was not blocked: %v", err)
			}
			if _, err := child.ExecContext(context.Background(), "UPDATE child"); err != nil {
				t.Fatal(err)
			}
			if err := child.Commit(); err != nil {
				t.Fatal(err)
			}
			if _, err := parent.ExecContext(context.Background(), "UPDATE parent"); err != nil {
				t.Fatal(err)
			}
			child, err = parent.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := parent.Commit(); err != nil {
				t.Fatal(err)
			}
			statements, _ := recorder.snapshot()
			if !reflect.DeepEqual(statements, test.want) {
				t.Fatalf("unexpected statements:\n got: %v\nwant: %v", statements, test.want)
			}
		})
	}
}

func TestTransactionCallbacksCommitRollbackAndCleanupChildren(t *testing.T) {
	connection, recorder := openTransactionConnection(t, db.PostgreSQL, nil)
	if err := connection.Transaction(context.Background(), nil, func(transaction *db.Transaction) error {
		return transaction.Transaction(context.Background(), func(child *db.Transaction) error {
			_, err := child.ExecContext(context.Background(), "UPDATE nested")
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}

	wantCallback := errors.New("callback failure")
	err := connection.Transaction(context.Background(), nil, func(transaction *db.Transaction) error {
		if _, beginErr := transaction.Begin(context.Background()); beginErr != nil {
			return beginErr
		}
		return wantCallback
	})
	if !errors.Is(err, wantCallback) {
		t.Fatalf("callback error was not preserved: %v", err)
	}

	statements, _ := recorder.snapshot()
	want := []string{
		"BEGIN", "SAVEPOINT chitu_sp_1", "UPDATE nested", "RELEASE SAVEPOINT chitu_sp_1", "COMMIT",
		"BEGIN", "SAVEPOINT chitu_sp_1", "ROLLBACK TO SAVEPOINT chitu_sp_1",
		"RELEASE SAVEPOINT chitu_sp_1", "ROLLBACK",
	}
	if !reflect.DeepEqual(statements, want) {
		t.Fatalf("unexpected callback lifecycle:\n got: %v\nwant: %v", statements, want)
	}
}

func TestTransactionCallbackRollsBackOnPanic(t *testing.T) {
	connection, recorder := openTransactionConnection(t, db.PostgreSQL, nil)
	func() {
		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Fatalf("unexpected recovered value: %v", recovered)
			}
		}()
		_ = connection.Transaction(context.Background(), nil, func(*db.Transaction) error {
			panic("boom")
		})
	}()
	statements, _ := recorder.snapshot()
	if want := []string{"BEGIN", "ROLLBACK"}; !reflect.DeepEqual(statements, want) {
		t.Fatalf("panic did not rollback transaction: %v", statements)
	}
}

func TestUnsupportedSavepointsAndValidation(t *testing.T) {
	registry, err := db.NewDialectRegistry(testDialect{name: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := openTransactionConnection(t, "custom", registry)
	transaction, err := connection.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.Begin(context.Background()); !errors.Is(err, db.ErrSavepointsUnsupported) {
		t.Fatalf("expected unsupported savepoints error, got %v", err)
	}
	if _, err := transaction.Begin(nil); err == nil { //nolint:staticcheck // SA1012: intentionally tests rejection of a nil context.
		t.Fatal("expected nil context error")
	}
	if err := transaction.Transaction(context.Background(), nil); err == nil {
		t.Fatal("expected nil callback error")
	}
	if err := connection.Transaction(context.Background(), nil, nil); err == nil {
		t.Fatal("expected nil root callback error")
	}
	if _, err := connection.BeginTx(nil, nil); err == nil { //nolint:staticcheck // SA1012: intentionally tests rejection of a nil context.
		t.Fatal("expected nil root context error")
	}
	var nilTransaction *db.Transaction
	if nilTransaction.Active() || nilTransaction.Depth() != -1 || !errors.Is(nilTransaction.Commit(), db.ErrTransactionDone) {
		t.Fatal("nil transaction validation failed")
	}
}

func TestSavepointAndDriverErrorsPreserveLifecycle(t *testing.T) {
	recorder := &transactionRecorder{fail: make(map[string]error)}
	connection, recorder := openTransactionConnectionWithRecorder(
		t, db.PostgreSQL, db.NewDefaultDialectRegistry(), recorder,
	)
	root, err := connection.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder.setFailure("SAVEPOINT chitu_sp_1", errTransactionSQL)
	if _, err := root.Begin(context.Background()); !errors.Is(err, errTransactionSQL) || !root.Active() {
		t.Fatalf("savepoint failure corrupted root: %v active=%v", err, root.Active())
	}
	recorder.setFailure("SAVEPOINT chitu_sp_1", nil)
	child, err := root.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recorder.setFailure("RELEASE SAVEPOINT chitu_sp_2", errTransactionSQL)
	if err := child.Commit(); !errors.Is(err, errTransactionSQL) || !child.Active() {
		t.Fatalf("release failure must leave child active: %v active=%v", err, child.Active())
	}
	recorder.setFailure("RELEASE SAVEPOINT chitu_sp_2", nil)
	if err := child.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := root.Rollback(); err != nil {
		t.Fatal(err)
	}

	commitRecorder := &transactionRecorder{fail: make(map[string]error), commitErr: errCommit}
	commitConnection, _ := openTransactionConnectionWithRecorder(
		t, db.PostgreSQL, db.NewDefaultDialectRegistry(), commitRecorder,
	)
	if err := commitConnection.Transaction(context.Background(), nil, func(*db.Transaction) error { return nil }); !errors.Is(err, errCommit) {
		t.Fatalf("commit error was not preserved: %v", err)
	}

	callbackErr := errors.New("callback error")
	rollbackRecorder := &transactionRecorder{fail: make(map[string]error), rollbackErr: errRollback}
	rollbackConnection, _ := openTransactionConnectionWithRecorder(
		t, db.PostgreSQL, db.NewDefaultDialectRegistry(), rollbackRecorder,
	)
	err = rollbackConnection.Transaction(context.Background(), nil, func(*db.Transaction) error { return callbackErr })
	if !errors.Is(err, callbackErr) || !errors.Is(err, errRollback) {
		t.Fatalf("callback and rollback errors were not joined: %v", err)
	}

	cleanupRecorder := &transactionRecorder{fail: map[string]error{
		"ROLLBACK TO SAVEPOINT chitu_sp_1": errTransactionSQL,
	}}
	cleanupConnection, cleanupRecorder := openTransactionConnectionWithRecorder(
		t, db.PostgreSQL, db.NewDefaultDialectRegistry(), cleanupRecorder,
	)
	err = cleanupConnection.Transaction(context.Background(), nil, func(root *db.Transaction) error {
		return root.Transaction(context.Background(), func(*db.Transaction) error {
			return callbackErr
		})
	})
	if !errors.Is(err, callbackErr) || !errors.Is(err, errTransactionSQL) {
		t.Fatalf("savepoint rollback error was not preserved: %v", err)
	}
	statements, _ := cleanupRecorder.snapshot()
	wantCleanup := []string{
		"BEGIN", "SAVEPOINT chitu_sp_1", "ROLLBACK TO SAVEPOINT chitu_sp_1", "ROLLBACK",
	}
	if !reflect.DeepEqual(statements, wantCleanup) {
		t.Fatalf("failed savepoint rollback did not force root rollback: %v", statements)
	}
}

func openTransactionConnection(t *testing.T, dialectName string, registry *db.DialectRegistry) (*db.Connection, *transactionRecorder) {
	t.Helper()
	if registry == nil {
		registry = db.NewDefaultDialectRegistry()
	}
	recorder := &transactionRecorder{fail: make(map[string]error)}
	return openTransactionConnectionWithRecorder(t, dialectName, registry, recorder)
}

func openTransactionConnectionWithRecorder(
	t *testing.T,
	dialectName string,
	registry *db.DialectRegistry,
	recorder *transactionRecorder,
) (*db.Connection, *transactionRecorder) {
	t.Helper()
	id := fmt.Sprintf("transaction-%d", transactionID.Add(1))
	transactionRecorders.Store(id, recorder)
	connection, err := db.Open(context.Background(), db.Config{
		Driver:  transactionDriverName,
		DSN:     id,
		Dialect: dialectName,
	}, registry)
	if err != nil {
		transactionRecorders.Delete(id)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
		transactionRecorders.Delete(id)
	})
	return connection, recorder
}

type transactionRecorder struct {
	mu          sync.Mutex
	statements  []string
	options     driver.TxOptions
	fail        map[string]error
	commitErr   error
	rollbackErr error
}

func (recorder *transactionRecorder) record(statement string) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.statements = append(recorder.statements, statement)
	return recorder.fail[statement]
}

func (recorder *transactionRecorder) setFailure(statement string, err error) {
	recorder.mu.Lock()
	if err == nil {
		delete(recorder.fail, statement)
	} else {
		recorder.fail[statement] = err
	}
	recorder.mu.Unlock()
}

func (recorder *transactionRecorder) snapshot() ([]string, driver.TxOptions) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.statements...), recorder.options
}

type transactionDriver struct{}

func (transactionDriver) Open(name string) (driver.Conn, error) {
	value, exists := transactionRecorders.Load(name)
	if !exists {
		return nil, fmt.Errorf("unknown transaction recorder %q", name)
	}
	return &transactionDriverConnection{recorder: value.(*transactionRecorder)}, nil
}

type transactionDriverConnection struct {
	recorder *transactionRecorder
}

func (connection *transactionDriverConnection) Prepare(query string) (driver.Stmt, error) {
	if err := connection.recorder.record("PREPARE " + query); err != nil {
		return nil, err
	}
	return transactionDriverStatement{}, nil
}
func (*transactionDriverConnection) Close() error { return nil }
func (connection *transactionDriverConnection) Begin() (driver.Tx, error) {
	return connection.BeginTx(context.Background(), driver.TxOptions{})
}
func (*transactionDriverConnection) Ping(context.Context) error { return nil }
func (connection *transactionDriverConnection) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	connection.recorder.mu.Lock()
	connection.recorder.options = options
	connection.recorder.mu.Unlock()
	if err := connection.recorder.record("BEGIN"); err != nil {
		return nil, err
	}
	return &transactionDriverTransaction{recorder: connection.recorder}, nil
}
func (connection *transactionDriverConnection) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if err := connection.recorder.record(query); err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}
func (connection *transactionDriverConnection) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := connection.recorder.record(query); err != nil {
		return nil, err
	}
	return &transactionDriverRows{}, nil
}

type transactionDriverTransaction struct {
	recorder *transactionRecorder
}

func (transaction *transactionDriverTransaction) Commit() error {
	_ = transaction.recorder.record("COMMIT")
	return transaction.recorder.commitErr
}
func (transaction *transactionDriverTransaction) Rollback() error {
	_ = transaction.recorder.record("ROLLBACK")
	return transaction.recorder.rollbackErr
}

type transactionDriverStatement struct{}

func (transactionDriverStatement) Close() error  { return nil }
func (transactionDriverStatement) NumInput() int { return -1 }
func (transactionDriverStatement) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (transactionDriverStatement) Query([]driver.Value) (driver.Rows, error) {
	return &transactionDriverRows{}, nil
}

type transactionDriverRows struct{}

func (*transactionDriverRows) Columns() []string         { return []string{"value"} }
func (*transactionDriverRows) Close() error              { return nil }
func (*transactionDriverRows) Next([]driver.Value) error { return io.EOF }

var (
	_ driver.Driver         = transactionDriver{}
	_ driver.Conn           = (*transactionDriverConnection)(nil)
	_ driver.ConnBeginTx    = (*transactionDriverConnection)(nil)
	_ driver.Pinger         = (*transactionDriverConnection)(nil)
	_ driver.ExecerContext  = (*transactionDriverConnection)(nil)
	_ driver.QueryerContext = (*transactionDriverConnection)(nil)
)
