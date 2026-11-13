package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Queryer is implemented by *sql.DB, *sql.Tx and db.Connection.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// DBDependency fingerprints the complete ordered result of a read-only SQL query.
// Queries should include ORDER BY when row order is otherwise unspecified.
type DBDependency struct {
	DB    Queryer
	Query string
	Args  []any
}

func (dependency DBDependency) Fingerprint(ctx context.Context) (string, error) {
	if dependency.DB == nil {
		return "", errors.New("DB dependency has no database")
	}
	if dependency.Query == "" {
		return "", errors.New("DB dependency query is empty")
	}
	rows, err := dependency.DB.QueryContext(ctx, dependency.Query, dependency.Args...)
	if err != nil {
		return "", fmt.Errorf("execute DB dependency: %w", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return "", fmt.Errorf("read DB dependency columns: %w", err)
	}
	hash := sha256.New()
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for index := range values {
			targets[index] = &values[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return "", fmt.Errorf("scan DB dependency: %w", err)
		}
		for _, value := range values {
			writeFingerprintValue(hash, value)
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate DB dependency: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type fingerprintWriter interface{ Write([]byte) (int, error) }

func writeFingerprintValue(writer fingerprintWriter, value any) {
	var data []byte
	switch typed := value.(type) {
	case nil:
		data = []byte("null")
	case []byte:
		data = typed
	case string:
		data = []byte(typed)
	case int64:
		data = []byte(strconv.FormatInt(typed, 10))
	case float64:
		data = []byte(strconv.FormatFloat(typed, 'g', -1, 64))
	case bool:
		data = []byte(strconv.FormatBool(typed))
	default:
		data = []byte(fmt.Sprint(typed))
	}
	_, _ = writer.Write([]byte{byte(len(data) >> 24), byte(len(data) >> 16), byte(len(data) >> 8), byte(len(data))})
	_, _ = writer.Write(data)
}
