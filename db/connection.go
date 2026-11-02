package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config настраивает database/sql и может загружаться стандартным config.Loader.
type Config struct {
	Driver          string        `json:"driver" env:"DB_DRIVER" envRequired:"true"`
	DSN             string        `json:"dsn" env:"DB_DSN"`
	Dialect         string        `json:"dialect" env:"DB_DIALECT" envRequired:"true"`
	MaxOpenConns    int           `json:"maxOpenConns" env:"DB_MAX_OPEN_CONNS" envDefault:"0"`
	MaxIdleConns    int           `json:"maxIdleConns" env:"DB_MAX_IDLE_CONNS" envDefault:"0"`
	ConnMaxLifetime time.Duration `json:"connMaxLifetime" env:"DB_CONN_MAX_LIFETIME" envDefault:"0s"`
	ConnMaxIdleTime time.Duration `json:"connMaxIdleTime" env:"DB_CONN_MAX_IDLE_TIME" envDefault:"0s"`
	PingTimeout     time.Duration `json:"pingTimeout" env:"DB_PING_TIMEOUT" envDefault:"5s"`
}

// Validate проверяет конфигурацию до создания sql.DB.
func (config Config) Validate() error {
	if strings.TrimSpace(config.Driver) == "" {
		return errors.New("db: driver обязателен")
	}
	if strings.TrimSpace(config.Dialect) == "" {
		return errors.New("db: dialect обязателен")
	}
	if config.MaxOpenConns < 0 {
		return errors.New("db: MaxOpenConns не может быть отрицательным")
	}
	if config.MaxIdleConns < 0 {
		return errors.New("db: MaxIdleConns не может быть отрицательным")
	}
	if config.MaxOpenConns > 0 && config.MaxIdleConns > config.MaxOpenConns {
		return errors.New("db: MaxIdleConns не может превышать MaxOpenConns")
	}
	if config.ConnMaxLifetime < 0 {
		return errors.New("db: ConnMaxLifetime не может быть отрицательным")
	}
	if config.ConnMaxIdleTime < 0 {
		return errors.New("db: ConnMaxIdleTime не может быть отрицательным")
	}
	if config.PingTimeout < 0 {
		return errors.New("db: PingTimeout не может быть отрицательным")
	}
	return nil
}

// Connection объединяет настроенный sql.DB и выбранный SQL-диалект.
type Connection struct {
	database *sql.DB
	dialect  Dialect
	config   Config
}

// Open создаёт пул, применяет конфигурацию и проверяет подключение через PingContext.
// При ошибке проверки созданный sql.DB закрывается.
func Open(ctx context.Context, config Config, registry *DialectRegistry) (*Connection, error) {
	if ctx == nil {
		return nil, errors.New("db: context не может быть nil")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, errors.New("db: реестр диалектов не задан")
	}
	dialect, err := registry.Require(config.Dialect)
	if err != nil {
		return nil, err
	}

	database, err := sql.Open(config.Driver, config.DSN)
	if err != nil {
		return nil, fmt.Errorf("db: открыть driver %q: %w", config.Driver, err)
	}
	database.SetMaxOpenConns(config.MaxOpenConns)
	database.SetMaxIdleConns(config.MaxIdleConns)
	database.SetConnMaxLifetime(config.ConnMaxLifetime)
	database.SetConnMaxIdleTime(config.ConnMaxIdleTime)

	pingContext := ctx
	cancel := func() {}
	if config.PingTimeout > 0 {
		pingContext, cancel = context.WithTimeout(ctx, config.PingTimeout)
	}
	defer cancel()
	if err := database.PingContext(pingContext); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("db: проверка подключения: %w", err)
	}

	return &Connection{database: database, dialect: dialect, config: config}, nil
}

// DB возвращает низкоуровневый пул database/sql для расширенных сценариев.
func (connection *Connection) DB() *sql.DB { return connection.database }

// Dialect возвращает выбранный SQL-диалект.
func (connection *Connection) Dialect() Dialect { return connection.dialect }

// Config возвращает копию применённой конфигурации.
func (connection *Connection) Config() Config { return connection.config }

// Ping проверяет доступность базы данных.
func (connection *Connection) Ping(ctx context.Context) error {
	if err := connection.database.PingContext(ctx); err != nil {
		return fmt.Errorf("db: проверка подключения: %w", err)
	}
	return nil
}

// Stats возвращает статистику пула database/sql.
func (connection *Connection) Stats() sql.DBStats { return connection.database.Stats() }

// ExecContext выполняет запрос без возвращаемых строк.
func (connection *Connection) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	started := time.Now()
	result, err := connection.database.ExecContext(ctx, query, args...)
	observeQuery(ctx, "exec", query, args, started, err)
	return result, err
}

// QueryContext выполняет запрос, возвращающий строки.
func (connection *Connection) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	started := time.Now()
	rows, err := connection.database.QueryContext(ctx, query, args...)
	observeQuery(ctx, "query", query, args, started, err)
	return rows, err
}

// QueryRowContext выполняет запрос одной строки.
func (connection *Connection) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	started := time.Now()
	row := connection.database.QueryRowContext(ctx, query, args...)
	observeQuery(ctx, "query_row", query, args, started, nil)
	return row
}

// PrepareContext создаёт подготовленный запрос.
func (connection *Connection) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	started := time.Now()
	statement, err := connection.database.PrepareContext(ctx, query)
	observeQuery(ctx, "prepare", query, nil, started, err)
	return statement, err
}

// Close закрывает пул соединений.
func (connection *Connection) Close() error {
	if err := connection.database.Close(); err != nil {
		return fmt.Errorf("db: закрыть подключение: %w", err)
	}
	return nil
}
