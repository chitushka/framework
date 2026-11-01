package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type queryKind uint8

const (
	queryUnset queryKind = iota
	querySelect
	queryInsert
	queryUpdate
	queryDelete
)

// JoinType is a validated SQL join type.
type JoinType uint8

const (
	JoinInner JoinType = iota
	JoinLeft
	JoinRight
	JoinFull
)

type SortDirection uint8

const (
	SortAscending SortDirection = iota
	SortDescending
)

type queryColumn struct {
	identifier string
	raw        string
	alias      string
	args       []any
}

type tableReference struct {
	name  string
	alias string
}

type joinClause struct {
	type_ JoinType
	table tableReference
	on    Condition
}

type orderClause struct {
	column    string
	direction SortDirection
}

// QueryBuilder is an immutable fluent SQL builder. Every mutating-looking
// method returns an independent copy, so a base query can safely be branched.
type QueryBuilder struct {
	dialect Dialect
	kind    queryKind
	err     error

	distinct bool
	columns  []queryColumn
	table    tableReference
	joins    []joinClause
	where    Condition
	groupBy  []string
	having   Condition
	orderBy  []orderClause
	limit    *int
	offset   *int

	values  map[string]any
	allRows bool
}

// NewQueryBuilder creates a builder for the supplied dialect.
func NewQueryBuilder(dialect Dialect) *QueryBuilder {
	return &QueryBuilder{dialect: dialect}
}

func (b *QueryBuilder) clone() *QueryBuilder {
	if b == nil {
		return &QueryBuilder{err: errors.New("db: query builder must not be nil")}
	}
	copy := *b
	copy.columns = append([]queryColumn(nil), b.columns...)
	for i := range copy.columns {
		copy.columns[i].args = append([]any(nil), copy.columns[i].args...)
	}
	copy.joins = append([]joinClause(nil), b.joins...)
	copy.groupBy = append([]string(nil), b.groupBy...)
	copy.orderBy = append([]orderClause(nil), b.orderBy...)
	if b.limit != nil {
		value := *b.limit
		copy.limit = &value
	}
	if b.offset != nil {
		value := *b.offset
		copy.offset = &value
	}
	if b.values != nil {
		copy.values = make(map[string]any, len(b.values))
		for key, value := range b.values {
			copy.values[key] = value
		}
	}
	return &copy
}

func (b *QueryBuilder) fail(err error) *QueryBuilder {
	result := b.clone()
	if result.err == nil {
		result.err = err
	}
	return result
}

func (b *QueryBuilder) setKind(kind queryKind) *QueryBuilder {
	result := b.clone()
	if result.kind != queryUnset && result.kind != kind && result.err == nil {
		result.err = errors.New("db: query type is already selected")
		return result
	}
	result.kind = kind
	return result
}

func (b *QueryBuilder) Select(columns ...string) *QueryBuilder {
	result := b.setKind(querySelect)
	for _, column := range columns {
		result.columns = append(result.columns, queryColumn{identifier: column})
	}
	return result
}

// SelectAs adds a quoted identifier with a quoted alias.
func (b *QueryBuilder) SelectAs(column, alias string) *QueryBuilder {
	result := b.setKind(querySelect)
	result.columns = append(result.columns, queryColumn{identifier: column, alias: alias})
	return result
}

// SelectRaw adds a trusted SQL expression. Values still use safe placeholders.
func (b *QueryBuilder) SelectRaw(expression string, args ...any) *QueryBuilder {
	result := b.setKind(querySelect)
	result.columns = append(result.columns, queryColumn{raw: expression, args: append([]any(nil), args...)})
	return result
}

// SelectRawAs adds a trusted SQL expression with a safely quoted alias.
func (b *QueryBuilder) SelectRawAs(expression, alias string, args ...any) *QueryBuilder {
	result := b.setKind(querySelect)
	result.columns = append(result.columns, queryColumn{raw: expression, alias: alias, args: append([]any(nil), args...)})
	return result
}

func (b *QueryBuilder) Distinct() *QueryBuilder {
	result := b.clone()
	result.distinct = true
	return result
}

func (b *QueryBuilder) From(table string, alias ...string) *QueryBuilder {
	result := b.clone()
	if len(alias) > 1 {
		return result.fail(errors.New("db: From accepts at most one alias"))
	}
	result.table = tableReference{name: table}
	if len(alias) == 1 {
		result.table.alias = alias[0]
	}
	return result
}

func (b *QueryBuilder) Join(joinType JoinType, table, alias string, on Condition) *QueryBuilder {
	result := b.clone()
	result.joins = append(result.joins, joinClause{
		type_: joinType,
		table: tableReference{name: table, alias: alias},
		on:    on,
	})
	return result
}

func (b *QueryBuilder) Where(condition Condition) *QueryBuilder {
	result := b.clone()
	result.where = condition
	return result
}

func (b *QueryBuilder) AndWhere(condition Condition) *QueryBuilder {
	result := b.clone()
	if result.where == nil {
		result.where = condition
	} else {
		result.where = And(result.where, condition)
	}
	return result
}

func (b *QueryBuilder) OrWhere(condition Condition) *QueryBuilder {
	result := b.clone()
	if result.where == nil {
		result.where = condition
	} else {
		result.where = Or(result.where, condition)
	}
	return result
}

func (b *QueryBuilder) GroupBy(columns ...string) *QueryBuilder {
	result := b.clone()
	result.groupBy = append(result.groupBy, columns...)
	return result
}

func (b *QueryBuilder) Having(condition Condition) *QueryBuilder {
	result := b.clone()
	result.having = condition
	return result
}

func (b *QueryBuilder) OrderBy(column string, direction SortDirection) *QueryBuilder {
	result := b.clone()
	result.orderBy = append(result.orderBy, orderClause{column: column, direction: direction})
	return result
}

func (b *QueryBuilder) Limit(limit int) *QueryBuilder {
	if limit < 0 {
		return b.fail(errors.New("db: limit must not be negative"))
	}
	result := b.clone()
	result.limit = &limit
	return result
}

func (b *QueryBuilder) Offset(offset int) *QueryBuilder {
	if offset < 0 {
		return b.fail(errors.New("db: offset must not be negative"))
	}
	result := b.clone()
	result.offset = &offset
	return result
}

func (b *QueryBuilder) Insert(table string, values map[string]any) *QueryBuilder {
	result := b.setKind(queryInsert)
	result.table = tableReference{name: table}
	result.values = copyValues(values)
	return result
}

func (b *QueryBuilder) Update(table string, values map[string]any) *QueryBuilder {
	result := b.setKind(queryUpdate)
	result.table = tableReference{name: table}
	result.values = copyValues(values)
	return result
}

func (b *QueryBuilder) Delete(table string) *QueryBuilder {
	result := b.setKind(queryDelete)
	result.table = tableReference{name: table}
	return result
}

// AllRows explicitly permits UPDATE or DELETE without a WHERE clause.
func (b *QueryBuilder) AllRows() *QueryBuilder {
	result := b.clone()
	result.allRows = true
	return result
}

func copyValues(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// Build generates SQL and its separately bound arguments.
func (b *QueryBuilder) Build() (string, []any, error) {
	if b == nil {
		return "", nil, errors.New("db: query builder must not be nil")
	}
	if b.err != nil {
		return "", nil, b.err
	}
	if b.dialect == nil || isNilDialect(b.dialect) {
		return "", nil, errors.New("db: dialect must not be nil")
	}
	ctx := &queryBuildContext{dialect: b.dialect}
	var query string
	var err error
	switch b.kind {
	case querySelect:
		query, err = b.buildSelect(ctx)
	case queryInsert:
		query, err = b.buildInsert(ctx)
	case queryUpdate:
		query, err = b.buildUpdate(ctx)
	case queryDelete:
		query, err = b.buildDelete(ctx)
	default:
		err = errors.New("db: query type is not selected")
	}
	if err != nil {
		return "", nil, err
	}
	return query, append([]any(nil), ctx.args...), nil
}

func (b *QueryBuilder) buildSelect(ctx *queryBuildContext) (string, error) {
	var query strings.Builder
	query.WriteString("SELECT ")
	if b.distinct {
		query.WriteString("DISTINCT ")
	}
	if b.dialect.Name() == SQLServer && b.limit != nil && b.offset == nil {
		query.WriteString(fmt.Sprintf("TOP (%d) ", *b.limit))
	}
	columns := b.columns
	if len(columns) == 0 {
		columns = []queryColumn{{identifier: "*"}}
	}
	builtColumns := make([]string, len(columns))
	for i, column := range columns {
		var built string
		var err error
		if column.raw != "" {
			built, err = buildRawFragment(ctx, column.raw, column.args)
		} else {
			built, err = quoteIdentifierPath(ctx.dialect, column.identifier)
		}
		if err != nil {
			return "", err
		}
		if column.alias != "" {
			alias, err := quoteIdentifierPath(ctx.dialect, column.alias)
			if err != nil {
				return "", err
			}
			built += " AS " + alias
		}
		builtColumns[i] = built
	}
	query.WriteString(strings.Join(builtColumns, ", "))
	if b.table.name != "" {
		table, err := buildTable(ctx.dialect, b.table)
		if err != nil {
			return "", err
		}
		query.WriteString(" FROM ")
		query.WriteString(table)
	}
	if len(b.joins) > 0 && b.table.name == "" {
		return "", errors.New("db: JOIN requires FROM")
	}
	for _, join := range b.joins {
		built, err := buildJoin(ctx, join)
		if err != nil {
			return "", err
		}
		query.WriteByte(' ')
		query.WriteString(built)
	}
	if err := appendPredicate(&query, ctx, " WHERE ", b.where); err != nil {
		return "", err
	}
	if len(b.groupBy) > 0 {
		group, err := buildIdentifiers(ctx.dialect, b.groupBy)
		if err != nil {
			return "", err
		}
		query.WriteString(" GROUP BY ")
		query.WriteString(strings.Join(group, ", "))
	}
	if err := appendPredicate(&query, ctx, " HAVING ", b.having); err != nil {
		return "", err
	}
	if len(b.orderBy) > 0 {
		order, err := buildOrder(ctx.dialect, b.orderBy)
		if err != nil {
			return "", err
		}
		query.WriteString(" ORDER BY ")
		query.WriteString(strings.Join(order, ", "))
	}
	if err := b.appendPagination(&query); err != nil {
		return "", err
	}
	return query.String(), nil
}

func (b *QueryBuilder) buildInsert(ctx *queryBuildContext) (string, error) {
	if b.table.name == "" {
		return "", errors.New("db: INSERT table must not be empty")
	}
	if len(b.values) == 0 {
		return "", errors.New("db: INSERT values must not be empty")
	}
	table, err := quoteIdentifierPath(ctx.dialect, b.table.name)
	if err != nil {
		return "", err
	}
	keys := sortedKeys(b.values)
	columns, err := buildIdentifiers(ctx.dialect, keys)
	if err != nil {
		return "", err
	}
	placeholders := make([]string, len(keys))
	for i, key := range keys {
		placeholders[i] = ctx.argument(b.values[key])
	}
	return "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")", nil
}

func (b *QueryBuilder) buildUpdate(ctx *queryBuildContext) (string, error) {
	if b.table.name == "" {
		return "", errors.New("db: UPDATE table must not be empty")
	}
	if len(b.values) == 0 {
		return "", errors.New("db: UPDATE values must not be empty")
	}
	if b.where == nil && !b.allRows {
		return "", errors.New("db: UPDATE without WHERE requires AllRows")
	}
	table, err := quoteIdentifierPath(ctx.dialect, b.table.name)
	if err != nil {
		return "", err
	}
	keys := sortedKeys(b.values)
	assignments := make([]string, len(keys))
	for i, key := range keys {
		column, err := quoteIdentifierPath(ctx.dialect, key)
		if err != nil {
			return "", err
		}
		assignments[i] = column + " = " + ctx.argument(b.values[key])
	}
	var query strings.Builder
	query.WriteString("UPDATE " + table + " SET " + strings.Join(assignments, ", "))
	if err := appendPredicate(&query, ctx, " WHERE ", b.where); err != nil {
		return "", err
	}
	return query.String(), nil
}

func (b *QueryBuilder) buildDelete(ctx *queryBuildContext) (string, error) {
	if b.table.name == "" {
		return "", errors.New("db: DELETE table must not be empty")
	}
	if b.where == nil && !b.allRows {
		return "", errors.New("db: DELETE without WHERE requires AllRows")
	}
	table, err := quoteIdentifierPath(ctx.dialect, b.table.name)
	if err != nil {
		return "", err
	}
	var query strings.Builder
	query.WriteString("DELETE FROM " + table)
	if err := appendPredicate(&query, ctx, " WHERE ", b.where); err != nil {
		return "", err
	}
	return query.String(), nil
}

func buildTable(dialect Dialect, table tableReference) (string, error) {
	name, err := quoteIdentifierPath(dialect, table.name)
	if err != nil {
		return "", err
	}
	if table.alias == "" {
		return name, nil
	}
	alias, err := quoteIdentifierPath(dialect, table.alias)
	if err != nil {
		return "", err
	}
	return name + " AS " + alias, nil
}

func buildJoin(ctx *queryBuildContext, join joinClause) (string, error) {
	var operator string
	switch join.type_ {
	case JoinInner:
		operator = "INNER JOIN"
	case JoinLeft:
		operator = "LEFT JOIN"
	case JoinRight:
		operator = "RIGHT JOIN"
	case JoinFull:
		operator = "FULL JOIN"
	default:
		return "", errors.New("db: invalid join type")
	}
	if join.on == nil {
		return "", errors.New("db: JOIN condition must not be nil")
	}
	table, err := buildTable(ctx.dialect, join.table)
	if err != nil {
		return "", err
	}
	on, err := join.on.buildCondition(ctx)
	if err != nil {
		return "", err
	}
	return operator + " " + table + " ON " + on, nil
}

func appendPredicate(query *strings.Builder, ctx *queryBuildContext, prefix string, condition Condition) error {
	if condition == nil {
		return nil
	}
	predicate, err := condition.buildCondition(ctx)
	if err != nil {
		return err
	}
	query.WriteString(prefix)
	query.WriteString(predicate)
	return nil
}

func buildIdentifiers(dialect Dialect, identifiers []string) ([]string, error) {
	result := make([]string, len(identifiers))
	for i, identifier := range identifiers {
		quoted, err := quoteIdentifierPath(dialect, identifier)
		if err != nil {
			return nil, err
		}
		result[i] = quoted
	}
	return result, nil
}

func buildOrder(dialect Dialect, clauses []orderClause) ([]string, error) {
	result := make([]string, len(clauses))
	for i, clause := range clauses {
		column, err := quoteIdentifierPath(dialect, clause.column)
		if err != nil {
			return nil, err
		}
		switch clause.direction {
		case SortAscending:
			result[i] = column + " ASC"
		case SortDescending:
			result[i] = column + " DESC"
		default:
			return nil, errors.New("db: invalid sort direction")
		}
	}
	return result, nil
}

func (b *QueryBuilder) appendPagination(query *strings.Builder) error {
	if b.limit == nil && b.offset == nil {
		return nil
	}
	offset := 0
	if b.offset != nil {
		offset = *b.offset
	}
	switch b.dialect.Name() {
	case SQLServer:
		if b.limit != nil && b.offset == nil {
			return nil
		}
		if len(b.orderBy) == 0 {
			return errors.New("db: SQL Server OFFSET requires ORDER BY")
		}
		query.WriteString(fmt.Sprintf(" OFFSET %d ROWS", offset))
		if b.limit != nil {
			query.WriteString(fmt.Sprintf(" FETCH NEXT %d ROWS ONLY", *b.limit))
		}
	case Oracle:
		if b.offset != nil {
			query.WriteString(fmt.Sprintf(" OFFSET %d ROWS", offset))
		}
		if b.limit != nil {
			if b.offset == nil {
				query.WriteString(fmt.Sprintf(" FETCH FIRST %d ROWS ONLY", *b.limit))
			} else {
				query.WriteString(fmt.Sprintf(" FETCH NEXT %d ROWS ONLY", *b.limit))
			}
		}
	case MySQL:
		if b.limit != nil {
			query.WriteString(fmt.Sprintf(" LIMIT %d", *b.limit))
		} else {
			query.WriteString(" LIMIT 18446744073709551615")
		}
		if b.offset != nil {
			query.WriteString(fmt.Sprintf(" OFFSET %d", offset))
		}
	default:
		if b.limit != nil {
			query.WriteString(fmt.Sprintf(" LIMIT %d", *b.limit))
		}
		if b.offset != nil {
			query.WriteString(fmt.Sprintf(" OFFSET %d", offset))
		}
	}
	return nil
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// QueryExecutor is implemented by Connection and Transaction.
type QueryExecutor interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ExecExecutor is implemented by Connection and Transaction.
type ExecExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (b *QueryBuilder) QueryContext(ctx context.Context, executor QueryExecutor) (*sql.Rows, error) {
	if executor == nil {
		return nil, errors.New("db: query executor must not be nil")
	}
	if b == nil || b.kind != querySelect {
		return nil, errors.New("db: QueryContext requires a SELECT query")
	}
	query, args, err := b.Build()
	if err != nil {
		return nil, err
	}
	return executor.QueryContext(ctx, query, args...)
}

func (b *QueryBuilder) ExecContext(ctx context.Context, executor ExecExecutor) (sql.Result, error) {
	if executor == nil {
		return nil, errors.New("db: exec executor must not be nil")
	}
	if b == nil || b.kind == querySelect || b.kind == queryUnset {
		return nil, errors.New("db: ExecContext requires INSERT, UPDATE, or DELETE")
	}
	query, args, err := b.Build()
	if err != nil {
		return nil, err
	}
	return executor.ExecContext(ctx, query, args...)
}
