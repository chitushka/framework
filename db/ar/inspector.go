package ar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	frameworkdb "github.com/chitushka/framework/db"
)

// InspectOptions supplies non-portable schema facts that database/sql's
// ColumnTypes contract does not expose consistently across drivers.
type InspectOptions struct {
	PrimaryKeys   []string
	AutoIncrement string
	VersionColumn string
	Relations     []RelationSchema
}

// DatabaseInspector discovers columns through database/sql metadata and uses
// explicit per-table options for keys, versioning, and generated relations.
type DatabaseInspector struct {
	Connection *frameworkdb.Connection
	Tables     map[string]InspectOptions
}

func (inspector DatabaseInspector) Inspect(ctx context.Context, table string) (TableSchema, error) {
	if inspector.Connection == nil {
		return TableSchema{}, errors.New("ar: inspector connection must not be nil")
	}
	if err := validateIdentifierPath(table); err != nil {
		return TableSchema{}, err
	}
	quoted := quoteTable(inspector.Connection.Dialect(), table)
	rows, err := inspector.Connection.QueryContext(ctx, "SELECT * FROM "+quoted+" WHERE 1 = 0")
	if err != nil {
		return TableSchema{}, fmt.Errorf("ar: inspect columns for %s: %w", table, err)
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return TableSchema{}, fmt.Errorf("ar: inspect column types for %s: %w", table, err)
	}
	options := inspector.Tables[table]
	primary := make(map[string]struct{}, len(options.PrimaryKeys))
	for _, column := range options.PrimaryKeys {
		primary[column] = struct{}{}
	}
	schema := TableSchema{Name: table, Columns: make([]ColumnSchema, len(types)), Relations: append([]RelationSchema(nil), options.Relations...)}
	for i, type_ := range types {
		nullable, known := type_.Nullable()
		_, isPrimary := primary[type_.Name()]
		schema.Columns[i] = ColumnSchema{
			Name:          type_.Name(),
			DataType:      type_.DatabaseTypeName(),
			Nullable:      known && nullable,
			PrimaryKey:    isPrimary,
			AutoIncrement: type_.Name() == options.AutoIncrement,
			Version:       type_.Name() == options.VersionColumn,
		}
	}
	return schema, nil
}

func quoteTable(dialect frameworkdb.Dialect, table string) string {
	parts := strings.Split(table, ".")
	for i, part := range parts {
		parts[i] = dialect.QuoteIdentifier(part)
	}
	return strings.Join(parts, ".")
}
