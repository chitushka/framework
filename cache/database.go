package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

// SQLDatabase is implemented by *sql.DB and framework db.Connection.
type SQLDatabase interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type DatabaseConfig struct {
	DB      SQLDatabase
	Dialect frameworkdb.Dialect
	Table   string
}

// DatabaseBackend stores entries in a relational table shared by application nodes.
type DatabaseBackend struct {
	database SQLDatabase
	dialect  frameworkdb.Dialect
	table    string
	keyCol   string
	valueCol string
	expCol   string
	now      func() time.Time
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func NewDatabaseBackend(config DatabaseConfig) (*DatabaseBackend, error) {
	if config.DB == nil {
		return nil, errors.New("cache: database is required")
	}
	if config.Dialect == nil {
		return nil, errors.New("cache: database dialect is required")
	}
	if config.Table == "" {
		config.Table = "cache"
	}
	quoted, err := quoteTable(config.Dialect, config.Table)
	if err != nil {
		return nil, err
	}
	return &DatabaseBackend{
		database: config.DB, dialect: config.Dialect, table: quoted, now: time.Now,
		keyCol: config.Dialect.QuoteIdentifier("cache_key"), valueCol: config.Dialect.QuoteIdentifier("value"),
		expCol: config.Dialect.QuoteIdentifier("expires_at"),
	}, nil
}

func (backend *DatabaseBackend) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if ctx == nil {
		return nil, false, errors.New("cache: context is nil")
	}
	query := "SELECT " + backend.valueCol + ", " + backend.expCol + " FROM " + backend.table + " WHERE " + backend.keyCol + " = " + backend.dialect.Placeholder(1)
	var value []byte
	var expires sql.NullInt64
	if err := backend.database.QueryRowContext(ctx, query, key).Scan(&value, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("cache: query database item: %w", err)
	}
	if expires.Valid && !backend.now().Before(time.Unix(0, expires.Int64)) {
		if err := backend.Delete(ctx, key); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (backend *DatabaseBackend) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	var expires any
	if ttl > 0 {
		expires = backend.now().Add(ttl).UnixNano()
	}
	update := "UPDATE " + backend.table + " SET " + backend.valueCol + " = " + backend.dialect.Placeholder(1) +
		", " + backend.expCol + " = " + backend.dialect.Placeholder(2) + " WHERE " + backend.keyCol + " = " + backend.dialect.Placeholder(3)
	result, err := backend.database.ExecContext(ctx, update, value, expires, key)
	if err != nil {
		return fmt.Errorf("cache: update database item: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("cache: inspect database update: %w", err)
	}
	if affected > 0 {
		return nil
	}
	insert := "INSERT INTO " + backend.table + " (" + backend.keyCol + ", " + backend.valueCol + ", " + backend.expCol + ") VALUES (" +
		backend.dialect.Placeholder(1) + ", " + backend.dialect.Placeholder(2) + ", " + backend.dialect.Placeholder(3) + ")"
	_, insertErr := backend.database.ExecContext(ctx, insert, key, value, expires)
	if insertErr == nil {
		return nil
	}
	// Another node can insert the same key between UPDATE and INSERT. Retrying the
	// update resolves that race without requiring dialect-specific UPSERT syntax.
	result, err = backend.database.ExecContext(ctx, update, value, expires, key)
	if err != nil {
		return fmt.Errorf("cache: insert or update database item: %w", err)
	}
	affected, err = result.RowsAffected()
	if err != nil || affected == 0 {
		return fmt.Errorf("cache: insert database item: %w", insertErr)
	}
	return nil
}

func (backend *DatabaseBackend) Delete(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	query := "DELETE FROM " + backend.table + " WHERE " + backend.keyCol + " = " + backend.dialect.Placeholder(1)
	if _, err := backend.database.ExecContext(ctx, query, key); err != nil {
		return fmt.Errorf("cache: delete database item: %w", err)
	}
	return nil
}

// SchemaSQL returns portable-enough DDL for the configured built-in dialect.
// Applications should execute it through their normal migration workflow.
func (backend *DatabaseBackend) SchemaSQL() string {
	valueType := "BLOB"
	switch backend.dialect.Name() {
	case frameworkdb.PostgreSQL:
		valueType = "BYTEA"
	case frameworkdb.MySQL:
		valueType = "LONGBLOB"
	case frameworkdb.SQLServer:
		valueType = "VARBINARY(MAX)"
	case frameworkdb.Oracle:
		valueType = "BLOB"
	}
	return "CREATE TABLE " + backend.table + " (" + backend.keyCol + " VARCHAR(255) PRIMARY KEY, " + backend.valueCol + " " + valueType + " NOT NULL, " + backend.expCol + " BIGINT NULL)"
}

func quoteTable(dialect frameworkdb.Dialect, table string) (string, error) {
	parts := strings.Split(table, ".")
	quoted := make([]string, len(parts))
	for index, part := range parts {
		if !sqlIdentifier.MatchString(part) {
			return "", fmt.Errorf("cache: invalid database table %q", table)
		}
		quoted[index] = dialect.QuoteIdentifier(part)
	}
	return strings.Join(quoted, "."), nil
}
