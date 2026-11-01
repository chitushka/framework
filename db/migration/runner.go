package migration

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

const DefaultTable = "chitu_migration"

type State string

const (
	StatePending  State = "pending"
	StateApplied  State = "applied"
	StateModified State = "modified"
	StateMissing  State = "missing"
)

// Status describes a source migration or a history entry missing from source.
type Status struct {
	ID        string
	State     State
	AppliedAt time.Time
}

// Result describes one migration applied or reverted by a Runner call.
type Result struct {
	ID       string
	Duration time.Duration
}

// Options configures migration history storage.
type Options struct {
	Table string
	Now   func() time.Time
}

// Runner applies migrations and records each successful change atomically when
// the selected database supports transactional DDL.
type Runner struct {
	connection *frameworkdb.Connection
	source     Source
	table      string
	now        func() time.Time
}

func NewRunner(connection *frameworkdb.Connection, source Source, options Options) (*Runner, error) {
	if connection == nil {
		return nil, errors.New("migration: connection must not be nil")
	}
	if source == nil {
		return nil, errors.New("migration: source must not be nil")
	}
	table := options.Table
	if table == "" {
		table = DefaultTable
	}
	if _, err := quotedPath(connection.Dialect(), table); err != nil {
		return nil, fmt.Errorf("migration: history table: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Runner{connection: connection, source: source, table: table, now: now}, nil
}

// Status returns source and database history in version order.
func (runner *Runner) Status(ctx context.Context) ([]Status, error) {
	migrations, err := runner.load(ctx)
	if err != nil {
		return nil, err
	}
	history, err := runner.history(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]historyEntry, len(history))
	for _, entry := range history {
		byID[entry.ID] = entry
	}
	statuses := make([]Status, 0, len(migrations)+len(history))
	for _, migration := range migrations {
		status := Status{ID: migration.ID(), State: StatePending}
		if entry, found := byID[migration.ID()]; found {
			status.State = StateApplied
			status.AppliedAt = entry.AppliedAt
			if entry.Checksum != migration.Checksum() {
				status.State = StateModified
			}
			delete(byID, migration.ID())
		}
		statuses = append(statuses, status)
	}
	for _, entry := range byID {
		statuses = append(statuses, Status{ID: entry.ID, State: StateMissing, AppliedAt: entry.AppliedAt})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].ID < statuses[j].ID })
	return statuses, nil
}

// Up applies pending migrations. A zero limit applies all pending migrations.
func (runner *Runner) Up(ctx context.Context, limit int) ([]Result, error) {
	if limit < 0 {
		return nil, errors.New("migration: up limit must not be negative")
	}
	migrations, err := runner.load(ctx)
	if err != nil {
		return nil, err
	}
	history, err := runner.history(ctx)
	if err != nil {
		return nil, err
	}
	applied := make(map[string]historyEntry, len(history))
	for _, entry := range history {
		applied[entry.ID] = entry
	}
	pending := make([]Migration, 0)
	for _, migration := range migrations {
		if entry, found := applied[migration.ID()]; found {
			if entry.Checksum != migration.Checksum() {
				return nil, fmt.Errorf("migration: applied migration %s was modified", migration.ID())
			}
			continue
		}
		pending = append(pending, migration)
	}
	if limit > 0 && len(pending) > limit {
		pending = pending[:limit]
	}
	results := make([]Result, 0, len(pending))
	for _, migration := range pending {
		started := time.Now()
		if err := runner.apply(ctx, migration); err != nil {
			return results, err
		}
		results = append(results, Result{ID: migration.ID(), Duration: time.Since(started)})
	}
	return results, nil
}

// Down reverts the latest applied migrations. Steps must be positive.
func (runner *Runner) Down(ctx context.Context, steps int) ([]Result, error) {
	if steps <= 0 {
		return nil, errors.New("migration: down steps must be positive")
	}
	migrations, err := runner.load(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Migration, len(migrations))
	for _, migration := range migrations {
		byID[migration.ID()] = migration
	}
	history, err := runner.history(ctx)
	if err != nil {
		return nil, err
	}
	if len(history) > steps {
		history = history[len(history)-steps:]
	}
	results := make([]Result, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		entry := history[i]
		migration, found := byID[entry.ID]
		if !found {
			return results, fmt.Errorf("migration: applied migration %s is missing from source", entry.ID)
		}
		if entry.Checksum != migration.Checksum() {
			return results, fmt.Errorf("migration: applied migration %s was modified", entry.ID)
		}
		started := time.Now()
		if err := runner.revert(ctx, migration); err != nil {
			return results, err
		}
		results = append(results, Result{ID: migration.ID(), Duration: time.Since(started)})
	}
	return results, nil
}

func (runner *Runner) load(ctx context.Context) ([]Migration, error) {
	migrations, err := runner.source.Migrations(ctx)
	if err != nil {
		return nil, err
	}
	result := append([]Migration(nil), migrations...)
	seenIDs := make(map[string]struct{}, len(result))
	for _, migration := range result {
		if err := migration.validate(); err != nil {
			return nil, err
		}
		if _, found := seenIDs[migration.ID()]; found {
			return nil, fmt.Errorf("migration: duplicate migration %s", migration.ID())
		}
		seenIDs[migration.ID()] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID() < result[j].ID() })
	return result, nil
}

type historyEntry struct {
	ID        string
	Checksum  string
	AppliedAt time.Time
}

func (runner *Runner) history(ctx context.Context) ([]historyEntry, error) {
	if err := runner.ensureHistoryTable(ctx); err != nil {
		return nil, err
	}
	table, _ := quotedPath(runner.connection.Dialect(), runner.table)
	version := runner.connection.Dialect().QuoteIdentifier("version")
	checksum := runner.connection.Dialect().QuoteIdentifier("checksum")
	appliedAt := runner.connection.Dialect().QuoteIdentifier("applied_at")
	rows, err := runner.connection.QueryContext(ctx,
		"SELECT "+version+", "+checksum+", "+appliedAt+" FROM "+table+" ORDER BY "+version+" ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("migration: read history: %w", err)
	}
	defer rows.Close()
	entries := make([]historyEntry, 0)
	for rows.Next() {
		var entry historyEntry
		var timestamp string
		if err := rows.Scan(&entry.ID, &entry.Checksum, &timestamp); err != nil {
			return nil, fmt.Errorf("migration: scan history: %w", err)
		}
		entry.AppliedAt, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("migration: invalid applied time for %s: %w", entry.ID, err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migration: iterate history: %w", err)
	}
	return entries, nil
}

func (runner *Runner) ensureHistoryTable(ctx context.Context) error {
	dialect := runner.connection.Dialect()
	table, _ := quotedPath(dialect, runner.table)
	version := dialect.QuoteIdentifier("version")
	checksum := dialect.QuoteIdentifier("checksum")
	appliedAt := dialect.QuoteIdentifier("applied_at")
	columns := version + " VARCHAR(255) PRIMARY KEY, " + checksum + " CHAR(64) NOT NULL, " + appliedAt + " VARCHAR(35) NOT NULL"
	var statement string
	switch dialect.Name() {
	case frameworkdb.PostgreSQL, frameworkdb.MySQL:
		statement = "CREATE TABLE IF NOT EXISTS " + table + " (" + columns + ")"
	case frameworkdb.SQLServer:
		objectName := strings.ReplaceAll(runner.table, "'", "''")
		statement = "IF OBJECT_ID(N'" + objectName + "', N'U') IS NULL CREATE TABLE " + table + " (" + columns + ")"
	case frameworkdb.Oracle:
		create := strings.ReplaceAll("CREATE TABLE "+table+" ("+columns+")", "'", "''")
		statement = "BEGIN EXECUTE IMMEDIATE '" + create + "'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END;"
	default:
		statement = "CREATE TABLE IF NOT EXISTS " + table + " (" + columns + ")"
	}
	if _, err := runner.connection.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("migration: ensure history table: %w", err)
	}
	return nil
}

func (runner *Runner) apply(ctx context.Context, migration Migration) error {
	return runner.connection.Transaction(ctx, nil, func(transaction *frameworkdb.Transaction) error {
		if _, err := transaction.ExecContext(ctx, migration.UpSQL); err != nil {
			return fmt.Errorf("migration: apply %s: %w", migration.ID(), err)
		}
		dialect := transaction.Dialect()
		table, _ := quotedPath(dialect, runner.table)
		columns := []string{
			dialect.QuoteIdentifier("version"),
			dialect.QuoteIdentifier("checksum"),
			dialect.QuoteIdentifier("applied_at"),
		}
		values := []string{dialect.Placeholder(1), dialect.Placeholder(2), dialect.Placeholder(3)}
		_, err := transaction.ExecContext(ctx,
			"INSERT INTO "+table+" ("+strings.Join(columns, ", ")+") VALUES ("+strings.Join(values, ", ")+")",
			migration.ID(), migration.Checksum(), runner.now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("migration: record %s: %w", migration.ID(), err)
		}
		return nil
	})
}

func (runner *Runner) revert(ctx context.Context, migration Migration) error {
	return runner.connection.Transaction(ctx, nil, func(transaction *frameworkdb.Transaction) error {
		if _, err := transaction.ExecContext(ctx, migration.DownSQL); err != nil {
			return fmt.Errorf("migration: revert %s: %w", migration.ID(), err)
		}
		dialect := transaction.Dialect()
		table, _ := quotedPath(dialect, runner.table)
		version := dialect.QuoteIdentifier("version")
		_, err := transaction.ExecContext(ctx,
			"DELETE FROM "+table+" WHERE "+version+" = "+dialect.Placeholder(1), migration.ID(),
		)
		if err != nil {
			return fmt.Errorf("migration: remove history %s: %w", migration.ID(), err)
		}
		return nil
	})
}

func quotedPath(dialect frameworkdb.Dialect, identifier string) (string, error) {
	if dialect == nil || strings.TrimSpace(identifier) != identifier || identifier == "" {
		return "", fmt.Errorf("invalid identifier %q", identifier)
	}
	parts := strings.Split(identifier, ".")
	for i, part := range parts {
		if part == "" || !validIdentifierPart(part) {
			return "", fmt.Errorf("invalid identifier %q", identifier)
		}
		parts[i] = dialect.QuoteIdentifier(part)
	}
	return strings.Join(parts, "."), nil
}

func validIdentifierPart(value string) bool {
	for i, char := range value {
		if char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || i > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return value != ""
}
