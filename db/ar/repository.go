package ar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	frameworkdb "github.com/chitushka/framework/db"
)

var (
	ErrNotFound          = errors.New("ar: record not found")
	ErrStaleModel        = errors.New("ar: record was changed by another transaction")
	ErrMissingPrimaryKey = errors.New("ar: model has no primary key")
	ErrVersionOverflow   = errors.New("ar: optimistic lock version overflow")
)

// Validator is an optional model contract. Validation runs before any write;
// multi-model atomicity requires WithExecutor with an application transaction.
type Validator interface{ Validate(context.Context) error }

func validateModel[T any](ctx context.Context, model *T) error {
	if validator, ok := any(model).(Validator); ok {
		return validator.Validate(ctx)
	}
	return nil
}

type TableNamer interface{ TableName() string }

type Executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type Options struct{ Table string }

// Repository maps one Go struct type to one database table.
type Repository[T any] struct {
	executor Executor
	dialect  frameworkdb.Dialect
	table    string
	metadata *modelMetadata
}

func NewRepository[T any](connection *frameworkdb.Connection, options Options) (*Repository[T], error) {
	if connection == nil {
		return nil, errors.New("ar: connection must not be nil")
	}
	metadata, err := metadataFor[T]()
	if err != nil {
		return nil, err
	}
	table := strings.TrimSpace(options.Table)
	if table == "" {
		var model T
		if named, ok := any(model).(TableNamer); ok {
			table = named.TableName()
		} else if named, ok := any(&model).(TableNamer); ok {
			table = named.TableName()
		}
	}
	if table == "" {
		return nil, errors.New("ar: table must be set in Options or TableName")
	}
	if err := validateIdentifierPath(table); err != nil {
		return nil, err
	}
	return &Repository[T]{executor: connection, dialect: connection.Dialect(), table: table, metadata: metadata}, nil
}

// WithExecutor returns a copy that executes through a transaction or another compatible executor.
func (repository *Repository[T]) WithExecutor(executor Executor) (*Repository[T], error) {
	if repository == nil || executor == nil {
		return nil, errors.New("ar: repository and executor must not be nil")
	}
	copy := *repository
	copy.executor = executor
	return &copy, nil
}

func (repository *Repository[T]) Find() *Finder[T] {
	if repository == nil {
		return &Finder[T]{err: errors.New("ar: repository must not be nil")}
	}
	columns := make([]string, len(repository.metadata.fields))
	for i, field := range repository.metadata.fields {
		columns[i] = field.name
	}
	return &Finder[T]{
		repository: repository,
		builder:    frameworkdb.NewQueryBuilder(repository.dialect).Select(columns...).From(repository.table),
	}

}

func (repository *Repository[T]) FindByPK(ctx context.Context, values ...any) (*T, error) {
	if repository == nil {
		return nil, errors.New("ar: repository must not be nil")
	}
	if len(repository.metadata.primary) == 0 {
		return nil, ErrMissingPrimaryKey
	}
	if len(values) != len(repository.metadata.primary) {
		return nil, fmt.Errorf("ar: primary key expects %d values, got %d", len(repository.metadata.primary), len(values))
	}
	conditions := make([]frameworkdb.Condition, len(values))
	for i, value := range values {
		conditions[i] = frameworkdb.Eq(repository.metadata.fields[repository.metadata.primary[i]].name, value)
	}
	return repository.Find().Where(frameworkdb.And(conditions...)).One(ctx)
}

func (repository *Repository[T]) Save(ctx context.Context, model *T) error {
	if repository == nil || model == nil {
		return errors.New("ar: repository and model must not be nil")
	}
	if repository.metadata.auto >= 0 && isZero(repository.metadata.fieldValue(reflect.ValueOf(model).Elem(), repository.metadata.auto)) {
		return repository.Insert(ctx, model)
	}
	return repository.Update(ctx, model)
}

func (repository *Repository[T]) Insert(ctx context.Context, model *T) error {
	if repository == nil || model == nil {
		return errors.New("ar: repository and model must not be nil")
	}
	if err := validateModel(ctx, model); err != nil {
		return err
	}
	value := reflect.ValueOf(model).Elem()
	versionInitialized := false
	if repository.metadata.version >= 0 {
		version := repository.metadata.fieldValue(value, repository.metadata.version)
		if isZero(version) {
			setInteger(version, 1)
			versionInitialized = true
		}
	}
	values := make(map[string]any, len(repository.metadata.fields))
	for position, field := range repository.metadata.fields {
		if field.auto && isZero(repository.metadata.fieldValue(value, position)) {
			continue
		}
		values[field.name] = repository.metadata.fieldValue(value, position).Interface()
	}
	query := frameworkdb.NewQueryBuilder(repository.dialect).Insert(repository.table, values)
	result, err := repository.executeInsert(ctx, query, value)
	if err != nil {
		if versionInitialized {
			setInteger(repository.metadata.fieldValue(value, repository.metadata.version), 0)
		}
		return fmt.Errorf("ar: insert %s: %w", repository.table, err)
	}
	if repository.metadata.auto >= 0 && result != nil {
		field := repository.metadata.fieldValue(value, repository.metadata.auto)
		if isZero(field) {
			if id, idErr := result.LastInsertId(); idErr == nil {
				setInteger(field, id)
			}
		}
	}
	return nil
}

func (repository *Repository[T]) executeInsert(ctx context.Context, query *frameworkdb.QueryBuilder, value reflect.Value) (sql.Result, error) {
	if repository.metadata.auto < 0 || !isZero(repository.metadata.fieldValue(value, repository.metadata.auto)) {
		return query.ExecContext(ctx, repository.executor)
	}
	field := repository.metadata.fieldValue(value, repository.metadata.auto)
	column := repository.dialect.QuoteIdentifier(repository.metadata.fields[repository.metadata.auto].name)
	statement, args, err := query.Build()
	if err != nil {
		return nil, err
	}
	switch repository.dialect.Name() {
	case frameworkdb.PostgreSQL:
		rows, err := repository.executor.QueryContext(ctx, statement+" RETURNING "+column, args...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return nil, errors.New("ar: INSERT RETURNING produced no row")
		}
		if err := rows.Scan(field.Addr().Interface()); err != nil {
			return nil, err
		}
		return nil, nil
	case frameworkdb.SQLServer:
		marker := ") VALUES ("
		position := strings.Index(statement, marker)
		if position < 0 {
			return nil, errors.New("ar: cannot add SQL Server OUTPUT clause")
		}
		statement = statement[:position+1] + " OUTPUT INSERTED." + column + statement[position+1:]
		rows, err := repository.executor.QueryContext(ctx, statement, args...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			return nil, errors.New("ar: INSERT OUTPUT produced no row")
		}
		if err := rows.Scan(field.Addr().Interface()); err != nil {
			return nil, err
		}
		return nil, nil
	case frameworkdb.Oracle:
		args = append(args, sql.Out{Dest: field.Addr().Interface()})
		statement += " RETURNING " + column + " INTO " + repository.dialect.Placeholder(len(args))
		return repository.executor.ExecContext(ctx, statement, args...)
	default:
		return repository.executor.ExecContext(ctx, statement, args...)
	}
}

func (repository *Repository[T]) Update(ctx context.Context, model *T) error {
	if repository == nil || model == nil {
		return errors.New("ar: repository and model must not be nil")
	}
	if len(repository.metadata.primary) == 0 {
		return ErrMissingPrimaryKey
	}
	if err := validateModel(ctx, model); err != nil {
		return err
	}
	value := reflect.ValueOf(model).Elem()
	conditions := repository.identityConditions(value)
	values := make(map[string]any, len(repository.metadata.fields))
	var versionField reflect.Value
	var nextVersion reflect.Value
	if repository.metadata.version >= 0 {
		position := repository.metadata.version
		versionField = repository.metadata.fieldValue(value, position)
		conditions = append(conditions, frameworkdb.Eq(repository.metadata.fields[position].name, versionField.Interface()))
		nextVersion = reflect.New(versionField.Type()).Elem()
		if versionField.Kind() >= reflect.Uint && versionField.Kind() <= reflect.Uint64 {
			old := versionField.Uint()
			if old == math.MaxUint64 || nextVersion.OverflowUint(old+1) {
				return ErrVersionOverflow
			}
			nextVersion.SetUint(old + 1)
		} else {
			old := versionField.Int()
			if old == math.MaxInt64 || nextVersion.OverflowInt(old+1) {
				return ErrVersionOverflow
			}
			nextVersion.SetInt(old + 1)
		}
	}
	for position, field := range repository.metadata.fields {
		if field.primary || field.auto {
			continue
		}
		values[field.name] = repository.metadata.fieldValue(value, position).Interface()
		if field.version {
			values[field.name] = nextVersion.Interface()
		}
	}
	query := frameworkdb.NewQueryBuilder(repository.dialect).
		Update(repository.table, values).
		Where(frameworkdb.And(conditions...))
	result, err := query.ExecContext(ctx, repository.executor)
	if err != nil {
		return fmt.Errorf("ar: update %s: %w", repository.table, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ar: update %s rows affected: %w", repository.table, err)
	}
	if affected == 0 {
		return ErrStaleModel
	}
	if versionField.IsValid() {
		versionField.Set(nextVersion)
	}
	return nil
}

func (repository *Repository[T]) Delete(ctx context.Context, model *T) error {
	if repository == nil || model == nil {
		return errors.New("ar: repository and model must not be nil")
	}
	if len(repository.metadata.primary) == 0 {
		return ErrMissingPrimaryKey
	}
	value := reflect.ValueOf(model).Elem()
	conditions := repository.identityConditions(value)
	if repository.metadata.version >= 0 {
		position := repository.metadata.version
		conditions = append(conditions, frameworkdb.Eq(repository.metadata.fields[position].name, repository.metadata.fieldValue(value, position).Interface()))
	}
	query := frameworkdb.NewQueryBuilder(repository.dialect).
		Delete(repository.table).
		Where(frameworkdb.And(conditions...))
	result, err := query.ExecContext(ctx, repository.executor)
	if err != nil {
		return fmt.Errorf("ar: delete %s: %w", repository.table, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ar: delete %s rows affected: %w", repository.table, err)
	}
	if affected == 0 {
		return ErrStaleModel
	}
	return nil
}

func (repository *Repository[T]) identityConditions(value reflect.Value) []frameworkdb.Condition {
	conditions := make([]frameworkdb.Condition, len(repository.metadata.primary))
	for i, position := range repository.metadata.primary {
		conditions[i] = frameworkdb.Eq(repository.metadata.fields[position].name, repository.metadata.fieldValue(value, position).Interface())
	}
	return conditions
}

func isZero(value reflect.Value) bool { return value.IsZero() }

func setInteger(value reflect.Value, number int64) {
	if value.Kind() >= reflect.Uint && value.Kind() <= reflect.Uint64 {
		value.SetUint(uint64(number))
	} else {
		value.SetInt(number)
	}
}

func validateIdentifierPath(identifier string) error {
	if identifier == "" || strings.TrimSpace(identifier) != identifier {
		return fmt.Errorf("ar: invalid table identifier %q", identifier)
	}
	for _, part := range strings.Split(identifier, ".") {
		if part == "" {
			return fmt.Errorf("ar: invalid table identifier %q", identifier)
		}
		for i, char := range part {
			if char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || i > 0 && char >= '0' && char <= '9' {
				continue
			}
			return fmt.Errorf("ar: invalid table identifier %q", identifier)
		}
	}
	return nil
}
