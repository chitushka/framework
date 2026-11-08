package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

const advisoryKey int64 = 0x43484954554d4947

// Locks are intentionally coarse: all framework migration histories in a
// PostgreSQL database / MySQL schema serialize, including qualified aliases.
func (runner *Runner) withLock(ctx context.Context, operation func(*Runner) error) (err error) {
	if runner == nil {
		return errors.New("migration: runner is required")
	}
	if ctx == nil {
		return errors.New("migration: context is required")
	}
	wait, cancel := context.WithTimeout(ctx, runner.lockTimeout)
	defer cancel()
	conn, err := runner.connection.DB().Conn(wait)
	if err != nil {
		return fmt.Errorf("migration: reserve connection: %w", err)
	}
	defer func() { _ = conn.Close() }()
	locked := *runner
	locked.session = conn
	var schema string
	if runner.connection.Dialect().Name() == frameworkdb.MySQL {
		parts := strings.Split(runner.table, ".")
		if len(parts) == 2 {
			schema = parts[0]
		} else if err = conn.QueryRowContext(wait, "SELECT DATABASE()").Scan(&schema); err != nil {
			discard(conn)
			return fmt.Errorf("migration: schema: %w", err)
		}
		if schema == "" {
			return errors.New("migration: a database/schema is required")
		}
	}
	hash := sha256.Sum256([]byte(strings.ToLower(schema)))
	name := "chitu:migrate:" + hex.EncodeToString(hash[:24])
	if err = locked.acquire(wait, name); err != nil {
		// A cancelled acquire may have succeeded on the server. Never put that
		// physical session back into the pool with uncertain ownership.
		discard(conn)
		return fmt.Errorf("migration: acquire lock: %w", err)
	}
	cancel() // the acquisition budget must not bound migration execution
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), runner.releaseTimeout)
		defer stop()
		if releaseErr := locked.release(cleanup, name); releaseErr != nil {
			discard(conn)
			err = errors.Join(err, fmt.Errorf("migration: release lock (connection discarded): %w", releaseErr))
		}
	}()
	if runner.connection.Dialect().Name() == frameworkdb.PostgreSQL {
		quoted, _ := quotedPath(runner.connection.Dialect(), runner.table)
		err = conn.QueryRowContext(ctx, `SELECT COALESCE((SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass($1)), current_schema())`, quoted).Scan(&schema)
		if err != nil {
			return fmt.Errorf("migration: resolve history schema: %w", err)
		}
		parts := strings.Split(runner.table, ".")
		if len(parts) == 2 {
			schema = parts[0]
		}
		if strings.HasPrefix(schema, "pg_") {
			return errors.New("migration: system/temporary history schemas are unsupported")
		}
	}
	parts := strings.Split(runner.table, ".")
	locked.table = schema + "." + parts[len(parts)-1]
	if _, err := quotedPath(runner.connection.Dialect(), locked.table); err != nil {
		return fmt.Errorf("migration: resolved schema: %w", err)
	}
	locked.dirtyTable = locked.table + "_state"
	if err := locked.ensureHistoryTable(ctx); err != nil {
		return err
	}
	if err := locked.ensureDirtyTable(ctx); err != nil {
		return err
	}
	if runner.connection.Dialect().Name() == frameworkdb.MySQL {
		for _, table := range []string{parts[len(parts)-1], parts[len(parts)-1] + "_state"} {
			var engine string
			if err := conn.QueryRowContext(ctx, "SELECT ENGINE FROM information_schema.tables WHERE table_schema = ? AND table_name = ?", schema, table).Scan(&engine); err != nil {
				return fmt.Errorf("migration: inspect table engine: %w", err)
			}
			if !strings.EqualFold(engine, "InnoDB") {
				return errors.New("migration: history/state tables must use InnoDB")
			}
		}
	}
	return operation(&locked)
}

func discard(conn *sql.Conn) { _ = conn.Raw(func(any) error { return driver.ErrBadConn }) }

func (runner *Runner) acquire(ctx context.Context, name string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if runner.connection.Dialect().Name() == frameworkdb.PostgreSQL {
			var acquired bool
			if err := runner.session.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", advisoryKey).Scan(&acquired); err != nil {
				return err
			}
			if acquired {
				return nil
			}
		} else {
			var acquired sql.NullInt64
			if err := runner.session.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&acquired); err != nil {
				return err
			}
			if !acquired.Valid {
				return errors.New("GET_LOCK returned NULL")
			}
			if acquired.Int64 == 1 {
				return nil
			}
			if acquired.Int64 != 0 {
				return errors.New("invalid GET_LOCK result")
			}
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (runner *Runner) release(ctx context.Context, name string) error {
	if runner.connection.Dialect().Name() == frameworkdb.PostgreSQL {
		var released bool
		if err := runner.session.QueryRowContext(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey).Scan(&released); err != nil {
			return err
		}
		if !released {
			return errors.New("advisory lock was not owned")
		}
	} else {
		var released sql.NullInt64
		if err := runner.session.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", name).Scan(&released); err != nil {
			return err
		}
		if !released.Valid || released.Int64 != 1 {
			return errors.New("named lock was not owned")
		}
	}
	return nil
}
