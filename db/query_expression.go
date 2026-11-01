package db

import (
	"errors"
	"fmt"
	"strings"
)

// Condition is a safely buildable SQL predicate. Values in the built-in
// conditions are always passed as query arguments, never interpolated into SQL.
type Condition interface {
	buildCondition(*queryBuildContext) (string, error)
}

type queryBuildContext struct {
	dialect Dialect
	args    []any
}

func (c *queryBuildContext) argument(value any) string {
	c.args = append(c.args, value)
	return c.dialect.Placeholder(len(c.args))
}

type comparisonCondition struct {
	column   string
	operator string
	value    any
}

func (c comparisonCondition) buildCondition(ctx *queryBuildContext) (string, error) {
	column, err := quoteIdentifierPath(ctx.dialect, c.column)
	if err != nil {
		return "", err
	}
	if c.value == nil {
		switch c.operator {
		case "=":
			return column + " IS NULL", nil
		case "<>":
			return column + " IS NOT NULL", nil
		default:
			return "", fmt.Errorf("db: operator %s cannot be used with nil", c.operator)
		}
	}
	return column + " " + c.operator + " " + ctx.argument(c.value), nil
}

// Eq creates an equality condition. A nil value is rendered as IS NULL.
func Eq(column string, value any) Condition {
	return comparisonCondition{column: column, operator: "=", value: value}
}

// Ne creates an inequality condition. A nil value is rendered as IS NOT NULL.
func Ne(column string, value any) Condition {
	return comparisonCondition{column: column, operator: "<>", value: value}
}

func Gt(column string, value any) Condition {
	return comparisonCondition{column: column, operator: ">", value: value}
}

func Gte(column string, value any) Condition {
	return comparisonCondition{column: column, operator: ">=", value: value}
}

func Lt(column string, value any) Condition {
	return comparisonCondition{column: column, operator: "<", value: value}
}

func Lte(column string, value any) Condition {
	return comparisonCondition{column: column, operator: "<=", value: value}
}

func Like(column string, value any) Condition {
	return comparisonCondition{column: column, operator: "LIKE", value: value}
}

type listCondition struct {
	column string
	values []any
	not    bool
}

func (c listCondition) buildCondition(ctx *queryBuildContext) (string, error) {
	column, err := quoteIdentifierPath(ctx.dialect, c.column)
	if err != nil {
		return "", err
	}
	if len(c.values) == 0 {
		if c.not {
			return "1 = 1", nil
		}
		return "1 = 0", nil
	}
	placeholders := make([]string, len(c.values))
	for i, value := range c.values {
		placeholders[i] = ctx.argument(value)
	}
	operator := " IN "
	if c.not {
		operator = " NOT IN "
	}
	return column + operator + "(" + strings.Join(placeholders, ", ") + ")", nil
}

func In(column string, values ...any) Condition {
	return listCondition{column: column, values: append([]any(nil), values...)}
}

func NotIn(column string, values ...any) Condition {
	return listCondition{column: column, values: append([]any(nil), values...), not: true}
}

type nullCondition struct {
	column string
	not    bool
}

func (c nullCondition) buildCondition(ctx *queryBuildContext) (string, error) {
	column, err := quoteIdentifierPath(ctx.dialect, c.column)
	if err != nil {
		return "", err
	}
	if c.not {
		return column + " IS NOT NULL", nil
	}
	return column + " IS NULL", nil
}

func IsNull(column string) Condition    { return nullCondition{column: column} }
func IsNotNull(column string) Condition { return nullCondition{column: column, not: true} }

type compoundCondition struct {
	operator   string
	conditions []Condition
}

func (c compoundCondition) buildCondition(ctx *queryBuildContext) (string, error) {
	if len(c.conditions) == 0 {
		if c.operator == "AND" {
			return "1 = 1", nil
		}
		return "1 = 0", nil
	}
	parts := make([]string, len(c.conditions))
	for i, condition := range c.conditions {
		if condition == nil {
			return "", errors.New("db: condition must not be nil")
		}
		part, err := condition.buildCondition(ctx)
		if err != nil {
			return "", err
		}
		parts[i] = "(" + part + ")"
	}
	return strings.Join(parts, " "+c.operator+" "), nil
}

func And(conditions ...Condition) Condition {
	return compoundCondition{operator: "AND", conditions: append([]Condition(nil), conditions...)}
}

func Or(conditions ...Condition) Condition {
	return compoundCondition{operator: "OR", conditions: append([]Condition(nil), conditions...)}
}

type notCondition struct{ condition Condition }

func (c notCondition) buildCondition(ctx *queryBuildContext) (string, error) {
	if c.condition == nil {
		return "", errors.New("db: condition must not be nil")
	}
	part, err := c.condition.buildCondition(ctx)
	if err != nil {
		return "", err
	}
	return "NOT (" + part + ")", nil
}

func Not(condition Condition) Condition { return notCondition{condition: condition} }

type rawCondition struct {
	sql  string
	args []any
}

func (c rawCondition) buildCondition(ctx *queryBuildContext) (string, error) {
	return buildRawFragment(ctx, c.sql, c.args)
}

// RawCondition creates a condition from a trusted SQL fragment. Each unescaped
// question mark is replaced with a dialect placeholder; use ?? for a literal ?.
// The SQL fragment itself must be developer-controlled.
func RawCondition(sql string, args ...any) Condition {
	return rawCondition{sql: sql, args: append([]any(nil), args...)}
}

func buildRawFragment(ctx *queryBuildContext, fragment string, args []any) (string, error) {
	if strings.TrimSpace(fragment) == "" {
		return "", errors.New("db: raw SQL fragment must not be empty")
	}
	var result strings.Builder
	argument := 0
	for i := 0; i < len(fragment); i++ {
		if fragment[i] != '?' {
			result.WriteByte(fragment[i])
			continue
		}
		if i+1 < len(fragment) && fragment[i+1] == '?' {
			result.WriteByte('?')
			i++
			continue
		}
		if argument >= len(args) {
			return "", errors.New("db: raw SQL has more placeholders than arguments")
		}
		result.WriteString(ctx.argument(args[argument]))
		argument++
	}
	if argument != len(args) {
		return "", errors.New("db: raw SQL has more arguments than placeholders")
	}
	return result.String(), nil
}

func quoteIdentifierPath(dialect Dialect, identifier string) (string, error) {
	if identifier == "" || strings.TrimSpace(identifier) != identifier || strings.ContainsRune(identifier, '\x00') {
		return "", fmt.Errorf("db: invalid identifier %q", identifier)
	}
	parts := strings.Split(identifier, ".")
	quoted := make([]string, len(parts))
	for i, part := range parts {
		if part == "" {
			return "", fmt.Errorf("db: invalid identifier %q", identifier)
		}
		if part == "*" {
			if i != len(parts)-1 {
				return "", fmt.Errorf("db: invalid wildcard identifier %q", identifier)
			}
			quoted[i] = part
			continue
		}
		quoted[i] = dialect.QuoteIdentifier(part)
	}
	return strings.Join(quoted, "."), nil
}
