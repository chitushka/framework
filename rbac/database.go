package rbac

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	frameworkdb "github.com/chitushka/framework/db"
)

// SQLDatabase is implemented by *sql.DB and db.Connection.
type SQLDatabase interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type DatabaseConfig struct {
	DB          SQLDatabase
	Dialect     frameworkdb.Dialect
	TablePrefix string
}

// DatabaseStore persists normalized RBAC state in three relational tables.
type DatabaseStore struct {
	database    SQLDatabase
	dialect     frameworkdb.Dialect
	itemTable   string
	childTable  string
	assignTable string
	nameCol     string
	typeCol     string
	descCol     string
	ruleCol     string
	parentCol   string
	childCol    string
	userCol     string
	roleCol     string
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func NewDatabaseStore(config DatabaseConfig) (*DatabaseStore, error) {
	if config.DB == nil {
		return nil, errors.New("rbac: database is required")
	}
	if config.Dialect == nil {
		return nil, errors.New("rbac: database dialect is required")
	}
	if config.TablePrefix == "" {
		config.TablePrefix = "auth"
	}
	if !identifierPattern.MatchString(config.TablePrefix) {
		return nil, fmt.Errorf("rbac: invalid table prefix %q", config.TablePrefix)
	}
	quote := config.Dialect.QuoteIdentifier
	return &DatabaseStore{
		database: config.DB, dialect: config.Dialect,
		itemTable: quote(config.TablePrefix + "_item"), childTable: quote(config.TablePrefix + "_item_child"), assignTable: quote(config.TablePrefix + "_assignment"),
		nameCol: quote("name"), typeCol: quote("type"), descCol: quote("description"), ruleCol: quote("rule_name"),
		parentCol: quote("parent"), childCol: quote("child"), userCol: quote("user_id"), roleCol: quote("role_name"),
	}, nil
}

func (store *DatabaseStore) Item(ctx context.Context, name string) (Item, bool, error) {
	if err := validateContext(ctx); err != nil {
		return Item{}, false, err
	}
	query := "SELECT " + store.nameCol + ", " + store.typeCol + ", " + store.descCol + ", " + store.ruleCol + " FROM " + store.itemTable + " WHERE " + store.nameCol + " = " + store.dialect.Placeholder(1)
	item, err := scanItem(store.database.QueryRowContext(ctx, query, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, fmt.Errorf("rbac: query item %q: %w", name, err)
	}
	return item, true, nil
}

func (store *DatabaseStore) Items(ctx context.Context, kind Kind) ([]Item, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	query := "SELECT " + store.nameCol + ", " + store.typeCol + ", " + store.descCol + ", " + store.ruleCol + " FROM " + store.itemTable
	var args []any
	if kind != "" {
		query += " WHERE " + store.typeCol + " = " + store.dialect.Placeholder(1)
		args = append(args, string(kind))
	}
	query += " ORDER BY " + store.nameCol
	rows, err := store.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("rbac: query items: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

func (store *DatabaseStore) AddItem(ctx context.Context, item Item) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	query := "INSERT INTO " + store.itemTable + " (" + strings.Join([]string{store.nameCol, store.typeCol, store.descCol, store.ruleCol}, ", ") + ") VALUES (" + placeholders(store.dialect, 4) + ")"
	if _, err := store.database.ExecContext(ctx, query, item.Name, string(item.Kind), nullable(item.Description), nullable(item.Rule)); err != nil {
		return fmt.Errorf("rbac: add item %q: %w", item.Name, err)
	}
	return nil
}

func (store *DatabaseStore) UpdateItem(ctx context.Context, item Item) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	query := "UPDATE " + store.itemTable + " SET " + store.typeCol + " = " + store.dialect.Placeholder(1) + ", " + store.descCol + " = " + store.dialect.Placeholder(2) + ", " + store.ruleCol + " = " + store.dialect.Placeholder(3) + " WHERE " + store.nameCol + " = " + store.dialect.Placeholder(4)
	result, err := store.database.ExecContext(ctx, query, string(item.Kind), nullable(item.Description), nullable(item.Rule), item.Name)
	if err != nil {
		return fmt.Errorf("rbac: update item %q: %w", item.Name, err)
	}
	return requireAffected(result, "item", item.Name)
}

func (store *DatabaseStore) RemoveItem(ctx context.Context, name string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	result, err := store.database.ExecContext(ctx, "DELETE FROM "+store.itemTable+" WHERE "+store.nameCol+" = "+store.dialect.Placeholder(1), name)
	if err != nil {
		return fmt.Errorf("rbac: remove item %q: %w", name, err)
	}
	return requireAffected(result, "item", name)
}

func (store *DatabaseStore) Children(ctx context.Context, parent string) ([]Item, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	query := "SELECT i." + store.nameCol + ", i." + store.typeCol + ", i." + store.descCol + ", i." + store.ruleCol + " FROM " + store.itemTable + " i JOIN " + store.childTable + " c ON c." + store.childCol + " = i." + store.nameCol + " WHERE c." + store.parentCol + " = " + store.dialect.Placeholder(1) + " ORDER BY i." + store.nameCol
	rows, err := store.database.QueryContext(ctx, query, parent)
	if err != nil {
		return nil, fmt.Errorf("rbac: query children of %q: %w", parent, err)
	}
	defer rows.Close()
	return scanItems(rows)
}

func (store *DatabaseStore) AddChild(ctx context.Context, parent, child string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	query := "INSERT INTO " + store.childTable + " (" + store.parentCol + ", " + store.childCol + ") VALUES (" + placeholders(store.dialect, 2) + ")"
	if _, err := store.database.ExecContext(ctx, query, parent, child); err != nil {
		return fmt.Errorf("rbac: add child %q to %q: %w", child, parent, err)
	}
	return nil
}

func (store *DatabaseStore) RemoveChild(ctx context.Context, parent, child string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	query := "DELETE FROM " + store.childTable + " WHERE " + store.parentCol + " = " + store.dialect.Placeholder(1) + " AND " + store.childCol + " = " + store.dialect.Placeholder(2)
	result, err := store.database.ExecContext(ctx, query, parent, child)
	if err != nil {
		return fmt.Errorf("rbac: remove child: %w", err)
	}
	return requireAffected(result, "child", child)
}

func (store *DatabaseStore) Assignments(ctx context.Context, userID string) ([]Assignment, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	query := "SELECT " + store.userCol + ", " + store.roleCol + " FROM " + store.assignTable + " WHERE " + store.userCol + " = " + store.dialect.Placeholder(1) + " ORDER BY " + store.roleCol
	rows, err := store.database.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("rbac: query assignments: %w", err)
	}
	defer rows.Close()
	var result []Assignment
	for rows.Next() {
		var assignment Assignment
		if err := rows.Scan(&assignment.UserID, &assignment.Role); err != nil {
			return nil, fmt.Errorf("rbac: scan assignment: %w", err)
		}
		result = append(result, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rbac: iterate assignments: %w", err)
	}
	return result, nil
}

func (store *DatabaseStore) Assign(ctx context.Context, assignment Assignment) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	query := "INSERT INTO " + store.assignTable + " (" + store.userCol + ", " + store.roleCol + ") VALUES (" + placeholders(store.dialect, 2) + ")"
	if _, err := store.database.ExecContext(ctx, query, assignment.UserID, assignment.Role); err != nil {
		return fmt.Errorf("rbac: assign role %q: %w", assignment.Role, err)
	}
	return nil
}

func (store *DatabaseStore) Revoke(ctx context.Context, assignment Assignment) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	query := "DELETE FROM " + store.assignTable + " WHERE " + store.userCol + " = " + store.dialect.Placeholder(1) + " AND " + store.roleCol + " = " + store.dialect.Placeholder(2)
	result, err := store.database.ExecContext(ctx, query, assignment.UserID, assignment.Role)
	if err != nil {
		return fmt.Errorf("rbac: revoke role %q: %w", assignment.Role, err)
	}
	return requireAffected(result, "assignment", assignment.Role)
}

// SchemaSQL returns DDL for the three normalized RBAC tables. Applications
// should apply it with their normal migration workflow.
func (store *DatabaseStore) SchemaSQL() string {
	item := "CREATE TABLE " + store.itemTable + " (" + store.nameCol + " VARCHAR(255) PRIMARY KEY, " + store.typeCol + " VARCHAR(16) NOT NULL, " + store.descCol + " VARCHAR(1024) NULL, " + store.ruleCol + " VARCHAR(255) NULL)"
	child := "CREATE TABLE " + store.childTable + " (" + store.parentCol + " VARCHAR(255) NOT NULL, " + store.childCol + " VARCHAR(255) NOT NULL, PRIMARY KEY (" + store.parentCol + ", " + store.childCol + "), FOREIGN KEY (" + store.parentCol + ") REFERENCES " + store.itemTable + " (" + store.nameCol + ") ON DELETE CASCADE, FOREIGN KEY (" + store.childCol + ") REFERENCES " + store.itemTable + " (" + store.nameCol + ") ON DELETE CASCADE)"
	assignment := "CREATE TABLE " + store.assignTable + " (" + store.userCol + " VARCHAR(255) NOT NULL, " + store.roleCol + " VARCHAR(255) NOT NULL, PRIMARY KEY (" + store.userCol + ", " + store.roleCol + "), FOREIGN KEY (" + store.roleCol + ") REFERENCES " + store.itemTable + " (" + store.nameCol + ") ON DELETE CASCADE)"
	return item + ";\n" + child + ";\n" + assignment + ";"
}

type rowScanner interface{ Scan(...any) error }

func scanItem(row rowScanner) (Item, error) {
	var item Item
	var kind string
	var description, rule sql.NullString
	if err := row.Scan(&item.Name, &kind, &description, &rule); err != nil {
		return Item{}, err
	}
	item.Kind = Kind(kind)
	item.Description = description.String
	item.Rule = rule.String
	return item, nil
}
func scanItems(rows *sql.Rows) ([]Item, error) {
	var result []Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("rbac: scan item: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rbac: iterate items: %w", err)
	}
	return result, nil
}
func placeholders(dialect frameworkdb.Dialect, count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = dialect.Placeholder(index + 1)
	}
	return strings.Join(values, ", ")
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func requireAffected(result sql.Result, kind, name string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rbac: inspect removed %s: %w", kind, err)
	}
	if affected == 0 {
		return fmt.Errorf("rbac: %s %q: %w", kind, name, ErrNotFound)
	}
	return nil
}
