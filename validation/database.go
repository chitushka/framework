package validation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	frameworkdb "github.com/chitushka/framework/db"
)

type QueryExecutor interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Database struct {
	Dialect  frameworkdb.Dialect
	Executor QueryExecutor
}

func DatabaseFrom(connection *frameworkdb.Connection) Database {
	if connection == nil {
		return Database{}
	}
	return Database{Dialect: connection.Dialect(), Executor: connection}
}

type Unique struct {
	Database Database
	Table    string
	Column   string
	// Ignore excludes the current record during updates, for example Ne("id", id).
	Ignore  frameworkdb.Condition
	Message string
}

func (validator Unique) Validate(ctx context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	exists, err := lookupExists(ctx, validator.Database, validator.Table, validator.Column, field.Value, validator.Ignore)
	if err != nil {
		return err
	}
	if exists {
		return Invalid("unique", messageOr(validator.Message, field.Field+" is already in use"))
	}
	return nil
}

type Exist struct {
	Database Database
	Table    string
	Column   string
	Filter   frameworkdb.Condition
	Message  string
}

func (validator Exist) Validate(ctx context.Context, field FieldContext) error {
	if isEmpty(field.Value) {
		return nil
	}
	exists, err := lookupExists(ctx, validator.Database, validator.Table, validator.Column, field.Value, validator.Filter)
	if err != nil {
		return err
	}
	if !exists {
		return Invalid("exist", messageOr(validator.Message, field.Field+" does not reference an existing record"))
	}
	return nil
}

func lookupExists(ctx context.Context, database Database, table, column string, value any, filter frameworkdb.Condition) (bool, error) {
	if database.Dialect == nil || database.Executor == nil {
		return false, errors.New("validation: database and executor must not be nil")
	}
	query := frameworkdb.NewQueryBuilder(database.Dialect).
		SelectRaw("1").
		From(table).
		Where(frameworkdb.Eq(column, value)).
		Limit(1)
	if filter != nil {
		query = query.AndWhere(filter)
	}
	rows, err := query.QueryContext(ctx, database.Executor)
	if err != nil {
		return false, fmt.Errorf("validation: database lookup: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return true, nil
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("validation: database lookup rows: %w", err)
	}
	return false, nil
}
