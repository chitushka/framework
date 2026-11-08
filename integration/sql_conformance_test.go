package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
)

func sqlBaseline(t *testing.T, run func(*testing.T, *db.Connection)) {
	t.Helper()
	for _, backend := range []struct{ dialect, driver, env string }{{db.PostgreSQL, "pgx", "POSTGRES_DSN"}, {db.MySQL, "mysql", "MYSQL_DSN"}} {
		t.Run(backend.dialect, func(t *testing.T) {
			c, err := db.Open(testContext(t), db.Config{Driver: backend.driver, DSN: required(t, backend.env), Dialect: backend.dialect, MaxOpenConns: 6, MaxIdleConns: 2}, db.NewDefaultDialectRegistry())
			check(t, err)
			t.Cleanup(func() { check(t, c.Close()) })
			run(t, c)
		})
	}
}

func sqlTable(t *testing.T, c *db.Connection, columns string) string {
	t.Helper()
	name := unique()
	quoted := c.Dialect().QuoteIdentifier(name)
	_, err := c.ExecContext(testContext(t), "CREATE TABLE "+quoted+" ("+columns+")")
	check(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := c.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoted)
		check(t, err)
	})
	return name
}

func TestSQLConformance(t *testing.T) {
	sqlBaseline(t, func(t *testing.T, c *db.Connection) {
		ctx := testContext(t)
		q, p := c.Dialect().QuoteIdentifier, c.Dialect().Placeholder
		table := sqlTable(t, c, q("id")+" BIGINT PRIMARY KEY, "+q("select")+" VARCHAR(255), amount DECIMAL(20,6), happened TIMESTAMP(6)")
		stamp := time.Date(2026, 11, 8, 12, 30, 22, 123456000, time.UTC)
		query := db.NewQueryBuilder(c.Dialect()).Insert(table, map[string]any{"id": 1, "select": nil, "amount": "123456789012.123456", "happened": stamp})
		result, err := query.ExecContext(ctx, c)
		check(t, err)
		n, err := result.RowsAffected()
		check(t, err)
		if n != 1 {
			t.Fatalf("insert rows=%d", n)
		}
		var nullable sql.NullString
		var amount string
		var actual time.Time
		check(t, c.QueryRowContext(ctx, "SELECT "+q("select")+", amount, happened FROM "+q(table)+" WHERE id="+p(1), 1).Scan(&nullable, &amount, &actual))
		if nullable.Valid || amount != "123456789012.123456" || !stamp.Equal(actual) {
			t.Fatalf("NULL/decimal/time: %v %s %v", nullable, amount, actual)
		}
		value := "' OR 1=1 -- Привет"
		result, err = db.NewQueryBuilder(c.Dialect()).Update(table, map[string]any{"select": value}).Where(db.Eq("id", 1)).ExecContext(ctx, c)
		check(t, err)
		n, err = result.RowsAffected()
		check(t, err)
		if n != 1 {
			t.Fatalf("changed rows=%d", n)
		}
		var text string
		check(t, c.QueryRowContext(ctx, "SELECT "+q("select")+" FROM "+q(table)+" WHERE id="+p(1), 1).Scan(&text))
		if text != value {
			t.Fatal("parameter/identifier boundary failed")
		}

		t.Run("pool exhaustion and recovery", func(t *testing.T) {
			c.DB().SetMaxOpenConns(1)
			defer c.DB().SetMaxOpenConns(6)
			pinned, err := c.DB().Conn(ctx)
			check(t, err)
			deadline, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
			err = c.Ping(deadline)
			cancel()
			check(t, pinned.Close())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pool wait=%v", err)
			}
			check(t, c.Ping(ctx))
		})
		t.Run("in flight cancellation", func(t *testing.T) {
			deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
			defer cancel()
			statement := "SELECT pg_sleep(10)"
			if c.Dialect().Name() == db.MySQL {
				statement = "SELECT SLEEP(10)"
			}
			start := time.Now()
			_, err := c.ExecContext(deadline, statement)
			if err == nil || time.Since(start) > 2*time.Second {
				t.Fatalf("query cancellation: %v", err)
			}
			check(t, c.Ping(ctx))
		})
		t.Run("savepoint and outer rollback", func(t *testing.T) {
			abort := errors.New("abort")
			err := c.Transaction(ctx, nil, func(tx *db.Transaction) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO "+q(table)+" (id) VALUES ("+p(1)+")", 2)
				if err != nil {
					return err
				}
				err = tx.Transaction(ctx, func(child *db.Transaction) error {
					_, err := child.ExecContext(ctx, "INSERT INTO "+q(table)+" (id) VALUES ("+p(1)+")", 3)
					if err != nil {
						return err
					}
					return abort
				})
				if !errors.Is(err, abort) {
					return fmt.Errorf("savepoint result: %w", err)
				}
				return abort
			})
			if !errors.Is(err, abort) {
				t.Fatal(err)
			}
			var count int
			check(t, c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+q(table)).Scan(&count))
			if count != 1 {
				t.Fatalf("partial writes survived rollback: %d", count)
			}
		})
		for _, commit := range []bool{true, false} {
			t.Run(fmt.Sprintf("connection death commit=%t", commit), func(t *testing.T) {
				tx, err := c.BeginTx(ctx, nil)
				check(t, err)
				defer func() { _ = tx.Rollback() }()
				identitySQL := "SELECT pg_backend_pid()"
				if c.Dialect().Name() == db.MySQL {
					identitySQL = "SELECT CONNECTION_ID()"
				}
				rows, err := tx.QueryContext(ctx, identitySQL)
				check(t, err)
				if !rows.Next() {
					t.Fatal("missing connection ID")
				}
				var id int64
				check(t, rows.Scan(&id))
				check(t, rows.Close())
				if c.Dialect().Name() == db.PostgreSQL {
					var killed bool
					check(t, c.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", id).Scan(&killed))
					if !killed {
						t.Fatal("connection was not terminated")
					}
				} else {
					_, err = c.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", id))
					check(t, err)
				}
				_, _ = tx.ExecContext(ctx, "SELECT 1")
				if commit {
					err = tx.Commit()
				} else {
					err = tx.Rollback()
				}
				if err == nil {
					t.Fatal("connection death was reported as success")
				}
				check(t, c.Ping(ctx))
			})
		}
		t.Run("DDL rollback dialect", func(t *testing.T) {
			name := unique()
			defer func() { _, _ = c.ExecContext(ctx, "DROP TABLE IF EXISTS "+q(name)) }()
			abort := errors.New("rollback DDL")
			err := c.Transaction(ctx, nil, func(tx *db.Transaction) error {
				_, err := tx.ExecContext(ctx, "CREATE TABLE "+q(name)+" (id INTEGER)")
				if err != nil {
					return err
				}
				return abort
			})
			if !errors.Is(err, abort) {
				t.Fatal(err)
			}
			rows, err := c.QueryContext(ctx, "SELECT * FROM "+q(name))
			if rows != nil {
				check(t, rows.Close())
			}
			if (err == nil) != (c.Dialect().Name() == db.MySQL) {
				t.Fatalf("DDL atomicity mismatch: %v", err)
			}
		})
	})
}
