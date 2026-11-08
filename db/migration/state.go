package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

var ErrDirty = errors.New("migration: dirty state requires explicit recovery")

type DirtyError struct{ ID, Checksum, Direction string }

func (e *DirtyError) Error() string {
	return fmt.Sprintf("migration: dirty %s (%s, checksum %s); inspect database and resolve explicitly", e.ID, e.Direction, e.Checksum)
}
func (e *DirtyError) Unwrap() error { return ErrDirty }

type dirtyEntry struct {
	ID, Checksum, Direction string
	StartedAt               time.Time
}

func (runner *Runner) ensureDirtyTable(ctx context.Context) error {
	d := runner.connection.Dialect()
	table, _ := quotedPath(d, runner.dirtyTable)
	statement := "CREATE TABLE IF NOT EXISTS " + table + " (" + d.QuoteIdentifier("version") + " VARCHAR(255) PRIMARY KEY, " + d.QuoteIdentifier("checksum") + " CHAR(64) NOT NULL, " + d.QuoteIdentifier("direction") + " VARCHAR(4) NOT NULL, " + d.QuoteIdentifier("started_at") + " VARCHAR(35) NOT NULL)"
	if d.Name() == frameworkdb.MySQL {
		statement += " ENGINE=InnoDB"
	}
	if _, err := runner.session.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("migration: ensure state table: %w", err)
	}
	return nil
}

func (runner *Runner) readDirty(ctx context.Context) ([]dirtyEntry, error) {
	d := runner.connection.Dialect()
	table, _ := quotedPath(d, runner.dirtyTable)
	columns := []string{d.QuoteIdentifier("version"), d.QuoteIdentifier("checksum"), d.QuoteIdentifier("direction"), d.QuoteIdentifier("started_at")}
	rows, err := runner.session.QueryContext(ctx, "SELECT "+strings.Join(columns, ", ")+" FROM "+table+" ORDER BY "+columns[0]+" ASC")
	if err != nil {
		return nil, fmt.Errorf("migration: read dirty state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var entries []dirtyEntry
	for rows.Next() {
		var entry dirtyEntry
		var timestamp string
		if err := rows.Scan(&entry.ID, &entry.Checksum, &entry.Direction, &timestamp); err != nil {
			return nil, err
		}
		entry.StartedAt, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (runner *Runner) checkClean(ctx context.Context) error {
	entries, err := runner.readDirty(ctx)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return &DirtyError{ID: entries[0].ID, Checksum: entries[0].Checksum, Direction: entries[0].Direction}
	}
	return nil
}

// Resolve records the operator-verified final database state, never executes SQL
// from a migration, and requires the exact checksum from the durable dirty marker.
// applied=true records history; false removes history. It is not an automatic fix.
func (runner *Runner) Resolve(ctx context.Context, id, checksum string, applied bool) error {
	if id == "" || len(checksum) != 64 {
		return errors.New("migration: dirty ID and exact checksum are required")
	}
	return runner.withLock(ctx, func(locked *Runner) error {
		entries, err := locked.readDirty(ctx)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.ID == id {
				if entry.Checksum != checksum {
					return errors.New("migration: dirty checksum mismatch")
				}
				return locked.finishTransaction(ctx, id, checksum, applied)
			}
		}
		return errors.New("migration: no matching dirty migration")
	})
}

func (runner *Runner) transition(ctx context.Context, migration Migration, up bool) error {
	d := runner.connection.Dialect()
	table, _ := quotedPath(d, runner.dirtyTable)
	direction, statement := "up", migration.UpSQL
	if !up {
		direction, statement = "down", migration.DownSQL
	}
	columns := []string{d.QuoteIdentifier("version"), d.QuoteIdentifier("checksum"), d.QuoteIdentifier("direction"), d.QuoteIdentifier("started_at")}
	values := []string{d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4)}
	if _, err := runner.session.ExecContext(ctx, "INSERT INTO "+table+" ("+strings.Join(columns, ", ")+") VALUES ("+strings.Join(values, ", ")+")", migration.ID(), migration.Checksum(), direction, runner.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("migration: prepare %s %s: %w", direction, migration.ID(), err)
	}
	if d.Name() == frameworkdb.PostgreSQL && !migration.NonTransactional {
		tx, err := runner.session.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration: %s %s (dirty): %w", direction, migration.ID(), err)
		}
		if err := runner.finalize(ctx, tx, migration.ID(), migration.Checksum(), up); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration: commit %s (inspect dirty/history state): %w", migration.ID(), err)
		}
		return nil
	}
	if _, err := runner.session.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("migration: %s %s (dirty; SQL may be partially applied): %w", direction, migration.ID(), err)
	}
	return runner.finishTransaction(ctx, migration.ID(), migration.Checksum(), up)
}

func (runner *Runner) finishTransaction(ctx context.Context, id, checksum string, applied bool) error {
	tx, err := runner.session.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := runner.finalize(ctx, tx, id, checksum, applied); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration: finalize %s (inspect dirty/history state): %w", id, err)
	}
	return nil
}

func (runner *Runner) finalize(ctx context.Context, tx *sql.Tx, id, checksum string, applied bool) error {
	d := runner.connection.Dialect()
	table, _ := quotedPath(d, runner.table)
	state, _ := quotedPath(d, runner.dirtyTable)
	version := d.QuoteIdentifier("version")
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+version+" = "+d.Placeholder(1), id); err != nil {
		return err
	}
	if applied {
		columns := []string{version, d.QuoteIdentifier("checksum"), d.QuoteIdentifier("applied_at")}
		values := []string{d.Placeholder(1), d.Placeholder(2), d.Placeholder(3)}
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" ("+strings.Join(columns, ", ")+") VALUES ("+strings.Join(values, ", ")+")", id, checksum, runner.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+state+" WHERE "+version+" = "+d.Placeholder(1), id); err != nil {
		return err
	}
	return nil
}
