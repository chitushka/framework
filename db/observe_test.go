package db_test

import (
	"context"
	"sync"
	"testing"

	"github.com/chitushka/framework/db"
)

type queryObserver struct {
	mu     sync.Mutex
	events []db.QueryEvent
}

func (observer *queryObserver) ObserveQuery(_ context.Context, event db.QueryEvent) {
	observer.mu.Lock()
	observer.events = append(observer.events, event)
	observer.mu.Unlock()
}

func TestConnectionQueryObserver(t *testing.T) {
	connection, err := db.Open(context.Background(), db.Config{Driver: testDriverName, DSN: "success", Dialect: db.PostgreSQL}, db.NewDefaultDialectRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	observer := &queryObserver{}
	ctx := db.WithQueryObserver(context.Background(), observer)
	if _, err := connection.ExecContext(ctx, "UPDATE observed", 7); err != nil {
		t.Fatal(err)
	}
	rows, err := connection.QueryContext(ctx, "SELECT observed")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if err := connection.QueryRowContext(ctx, "SELECT value").Scan(new(string)); err != nil {
		t.Fatal(err)
	}
	statement, err := connection.PrepareContext(ctx, "UPDATE prepared")
	if err != nil {
		t.Fatal(err)
	}
	_ = statement.Close()
	if len(observer.events) != 4 || observer.events[0].Operation != "exec" || observer.events[0].Args[0] != 7 || observer.events[3].Operation != "prepare" {
		t.Fatalf("unexpected query events: %#v", observer.events)
	}
}

func TestAdditionalObserverPreservesExistingDiagnostics(t *testing.T) {
	connection, err := db.Open(t.Context(), db.Config{Driver: testDriverName, DSN: "success", Dialect: db.PostgreSQL}, db.NewDefaultDialectRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	first, second := &queryObserver{}, &queryObserver{}
	ctx := db.AddQueryObserver(db.WithQueryObserver(t.Context(), first), second)
	if _, err := connection.ExecContext(ctx, "UPDATE observed", 7); err != nil {
		t.Fatal(err)
	}
	if len(first.events) != 1 || len(second.events) != 1 {
		t.Fatal("SQL diagnostics observer was replaced")
	}
}

func TestTransactionQueryObserver(t *testing.T) {
	connection, _ := openTransactionConnection(t, db.PostgreSQL, nil)
	transaction, err := connection.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	observer := &queryObserver{}
	ctx := db.WithQueryObserver(context.Background(), observer)
	if _, err := transaction.ExecContext(ctx, "UPDATE observed"); err != nil {
		t.Fatal(err)
	}
	rows, err := transaction.QueryContext(ctx, "SELECT observed")
	if err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	statement, err := transaction.PrepareContext(ctx, "UPDATE prepared")
	if err != nil {
		t.Fatal(err)
	}
	_ = statement.Close()
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(observer.events) != 3 || observer.events[0].Operation != "tx_exec" || observer.events[1].Operation != "tx_query" || observer.events[2].Operation != "tx_prepare" {
		t.Fatalf("unexpected transaction events: %#v", observer.events)
	}
}
