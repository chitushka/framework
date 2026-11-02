package queue

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

type SQLDatabase interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type DatabaseConfig struct {
	DB                 SQLDatabase
	Dialect            frameworkdb.Dialect
	Table              string
	ReservationTimeout time.Duration
	PollInterval       time.Duration
}
type DatabaseBroker struct {
	database SQLDatabase
	dialect  frameworkdb.Dialect
	table    string
	config   DatabaseConfig
	now      func() time.Time
	columns  databaseColumns
}
type databaseColumns struct{ id, name, payload, attempt, maxAttempts, available, created, reservation, reserved string }

var databaseIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func NewDatabaseBroker(config DatabaseConfig) (*DatabaseBroker, error) {
	if config.DB == nil {
		return nil, errors.New("queue: database is required")
	}
	if config.Dialect == nil {
		return nil, errors.New("queue: database dialect is required")
	}
	if config.Table == "" {
		config.Table = "queue"
	}
	parts := strings.Split(config.Table, ".")
	quoted := make([]string, len(parts))
	for index, part := range parts {
		if !databaseIdentifier.MatchString(part) {
			return nil, fmt.Errorf("queue: invalid table %q", config.Table)
		}
		quoted[index] = config.Dialect.QuoteIdentifier(part)
	}
	if config.ReservationTimeout == 0 {
		config.ReservationTimeout = 5 * time.Minute
	}
	if config.ReservationTimeout < 0 {
		return nil, errors.New("queue: ReservationTimeout cannot be negative")
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.PollInterval < 0 {
		return nil, errors.New("queue: PollInterval cannot be negative")
	}
	q := config.Dialect.QuoteIdentifier
	return &DatabaseBroker{database: config.DB, dialect: config.Dialect, table: strings.Join(quoted, "."), config: config, now: time.Now, columns: databaseColumns{q("id"), q("name"), q("payload"), q("attempt"), q("max_attempts"), q("available_at"), q("created_at"), q("reservation"), q("reserved_at")}}, nil
}

func (broker *DatabaseBroker) Enqueue(ctx context.Context, message Message, delay time.Duration) error {
	if ctx == nil {
		return errors.New("queue: context is nil")
	}
	if delay < 0 {
		return errors.New("queue: delay cannot be negative")
	}
	if err := validateMessage(message); err != nil {
		return err
	}
	c := broker.columns
	query := "INSERT INTO " + broker.table + " (" + strings.Join([]string{c.id, c.name, c.payload, c.attempt, c.maxAttempts, c.available, c.created, c.reservation, c.reserved}, ", ") + ") VALUES (" + dbPlaceholders(broker.dialect, 9) + ")"
	_, err := broker.database.ExecContext(ctx, query, message.ID, message.Name, []byte(message.Payload), message.Attempt, message.MaxAttempts, broker.now().Add(delay).UnixNano(), message.CreatedAt.UnixNano(), nil, nil)
	if err != nil {
		return fmt.Errorf("queue: database enqueue: %w", err)
	}
	return nil
}

func (broker *DatabaseBroker) Reserve(ctx context.Context, wait time.Duration) (Delivery, error) {
	if ctx == nil {
		return nil, errors.New("queue: context is nil")
	}
	if wait < 0 {
		return nil, errors.New("queue: wait cannot be negative")
	}
	deadline := broker.now().Add(wait)
	for {
		delivery, found, err := broker.tryReserve(ctx)
		if err != nil {
			return nil, err
		}
		if found {
			return delivery, nil
		}
		remaining := deadline.Sub(broker.now())
		if wait == 0 || remaining <= 0 {
			return nil, ErrEmpty
		}
		pause := broker.config.PollInterval
		if pause > remaining {
			pause = remaining
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (broker *DatabaseBroker) tryReserve(ctx context.Context) (Delivery, bool, error) {
	c := broker.columns
	now := broker.now()
	cutoff := now.Add(-broker.config.ReservationTimeout).UnixNano()
	selectSQL := "SELECT " + strings.Join([]string{c.id, c.name, c.payload, c.attempt, c.maxAttempts, c.created}, ", ") + " FROM " + broker.table + " WHERE " + c.available + " <= " + broker.dialect.Placeholder(1) + " AND (" + c.reservation + " IS NULL OR " + c.reserved + " <= " + broker.dialect.Placeholder(2) + ") ORDER BY " + c.available + ", " + c.id
	switch broker.dialect.Name() {
	case frameworkdb.SQLServer:
		selectSQL = "SELECT TOP 1 " + strings.Join([]string{c.id, c.name, c.payload, c.attempt, c.maxAttempts, c.created}, ", ") + " FROM " + broker.table + " WHERE " + c.available + " <= " + broker.dialect.Placeholder(1) + " AND (" + c.reservation + " IS NULL OR " + c.reserved + " <= " + broker.dialect.Placeholder(2) + ") ORDER BY " + c.available + ", " + c.id
	case frameworkdb.Oracle:
		selectSQL += " FETCH FIRST 1 ROWS ONLY"
	default:
		selectSQL += " LIMIT 1"
	}
	var message Message
	var payload []byte
	var created int64
	err := broker.database.QueryRowContext(ctx, selectSQL, now.UnixNano(), cutoff).Scan(&message.ID, &message.Name, &payload, &message.Attempt, &message.MaxAttempts, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("queue: database select: %w", err)
	}
	message.Payload = payload
	message.CreatedAt = time.Unix(0, created).UTC()
	token, err := messageID()
	if err != nil {
		return nil, false, err
	}
	update := "UPDATE " + broker.table + " SET " + c.reservation + " = " + broker.dialect.Placeholder(1) + ", " + c.reserved + " = " + broker.dialect.Placeholder(2) + " WHERE " + c.id + " = " + broker.dialect.Placeholder(3) + " AND (" + c.reservation + " IS NULL OR " + c.reserved + " <= " + broker.dialect.Placeholder(4) + ")"
	result, err := broker.database.ExecContext(ctx, update, token, now.UnixNano(), message.ID, cutoff)
	if err != nil {
		return nil, false, fmt.Errorf("queue: database reserve: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("queue: database inspect reservation: %w", err)
	}
	if affected == 0 {
		return nil, false, nil
	}
	return &databaseDelivery{broker: broker, message: message, token: token}, true, nil
}

type databaseDelivery struct {
	broker     *DatabaseBroker
	message    Message
	token      string
	settlement settlement
}

func (delivery *databaseDelivery) Message() Message { return cloneMessage(delivery.message) }
func (delivery *databaseDelivery) Ack(ctx context.Context) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		c := delivery.broker.columns
		query := "DELETE FROM " + delivery.broker.table + " WHERE " + c.id + " = " + delivery.broker.dialect.Placeholder(1) + " AND " + c.reservation + " = " + delivery.broker.dialect.Placeholder(2)
		result, err := delivery.broker.database.ExecContext(ctx, query, delivery.message.ID, delivery.token)
		if err != nil {
			return fmt.Errorf("queue: database acknowledge: %w", err)
		}
		return requireQueueAffected(result)
	})
}
func (delivery *databaseDelivery) Nack(ctx context.Context, delay time.Duration) error {
	return delivery.settlement.run(func() error {
		if ctx == nil {
			return errors.New("queue: context is nil")
		}
		if delay < 0 {
			return errors.New("queue: delay cannot be negative")
		}
		b := delivery.broker
		c := b.columns
		query := "UPDATE " + b.table + " SET " + c.attempt + " = " + c.attempt + " + 1, " + c.available + " = " + b.dialect.Placeholder(1) + ", " + c.reservation + " = NULL, " + c.reserved + " = NULL WHERE " + c.id + " = " + b.dialect.Placeholder(2) + " AND " + c.reservation + " = " + b.dialect.Placeholder(3)
		result, err := b.database.ExecContext(ctx, query, b.now().Add(delay).UnixNano(), delivery.message.ID, delivery.token)
		if err != nil {
			return fmt.Errorf("queue: database retry: %w", err)
		}
		return requireQueueAffected(result)
	})
}
func requireQueueAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("queue: inspect affected rows: %w", err)
	}
	if affected != 1 {
		return errors.New("queue: delivery reservation was lost")
	}
	return nil
}
func dbPlaceholders(dialect frameworkdb.Dialect, count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = dialect.Placeholder(index + 1)
	}
	return strings.Join(values, ", ")
}
func (broker *DatabaseBroker) SchemaSQL() string {
	payloadType := "BLOB"
	switch broker.dialect.Name() {
	case frameworkdb.PostgreSQL:
		payloadType = "BYTEA"
	case frameworkdb.MySQL:
		payloadType = "LONGBLOB"
	case frameworkdb.SQLServer:
		payloadType = "VARBINARY(MAX)"
	}
	c := broker.columns
	return "CREATE TABLE " + broker.table + " (" + c.id + " VARCHAR(64) PRIMARY KEY, " + c.name + " VARCHAR(255) NOT NULL, " + c.payload + " " + payloadType + " NOT NULL, " + c.attempt + " INTEGER NOT NULL, " + c.maxAttempts + " INTEGER NOT NULL, " + c.available + " BIGINT NOT NULL, " + c.created + " BIGINT NOT NULL, " + c.reservation + " VARCHAR(64) NULL, " + c.reserved + " BIGINT NULL)"
}
