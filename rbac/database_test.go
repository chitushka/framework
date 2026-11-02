package rbac

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	frameworkdb "github.com/chitushka/framework/db"
)

func TestDatabaseStoreLifecycle(t *testing.T) {
	database := openRBACDatabase(t)
	defer database.Close()
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	store, err := NewDatabaseStore(DatabaseConfig{DB: database, Dialect: dialect, TablePrefix: "app_auth"})
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := New(store)
	ctx := context.Background()
	if err := manager.AddRole(ctx, "admin", "Administrator"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddPermission(ctx, "post.read", "Read posts"); err != nil {
		t.Fatal(err)
	}
	if err := manager.UpdateItem(ctx, Item{Name: "post.read", Kind: Permission, Description: "Read published posts"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddChild(ctx, "admin", "post.read"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Assign(ctx, "42", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveChild(ctx, "admin", "post.read"); err != nil {
		t.Fatal(err)
	}
	if err := manager.AddChild(ctx, "admin", "post.read"); err != nil {
		t.Fatal(err)
	}
	allowed, err := manager.CheckAccess(ctx, "42", "post.read", nil)
	if err != nil || !allowed {
		t.Fatalf("access = %v, %v", allowed, err)
	}
	items, err := store.Items(ctx, Permission)
	if err != nil || len(items) != 1 || items[0].Description != "Read published posts" {
		t.Fatalf("items = %#v, %v", items, err)
	}
	if err := manager.Revoke(ctx, "42", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveItem(ctx, "post.read"); err != nil {
		t.Fatal(err)
	}
	if ddl := store.SchemaSQL(); !strings.Contains(ddl, `CREATE TABLE "app_auth_item"`) || strings.Count(ddl, "CREATE TABLE") != 3 || !strings.Contains(ddl, "ON DELETE CASCADE") {
		t.Fatalf("schema = %q", ddl)
	}
}

func TestDatabaseStoreValidationAndDialects(t *testing.T) {
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	if _, err := NewDatabaseStore(DatabaseConfig{Dialect: dialect}); err == nil {
		t.Fatal("nil database accepted")
	}
	database := openRBACDatabase(t)
	defer database.Close()
	if _, err := NewDatabaseStore(DatabaseConfig{DB: database, Dialect: dialect, TablePrefix: "bad;drop"}); err == nil {
		t.Fatal("unsafe prefix accepted")
	}
	for _, name := range []string{frameworkdb.MySQL, frameworkdb.Oracle, frameworkdb.SQLServer} {
		dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(name)
		store, err := NewDatabaseStore(DatabaseConfig{DB: database, Dialect: dialect})
		if err != nil || !strings.Contains(store.SchemaSQL(), "CREATE TABLE") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

var rbacDriverSequence atomic.Uint64

type rbacDBState struct {
	mu          sync.Mutex
	items       map[string]Item
	children    map[string]map[string]bool
	assignments map[string]map[string]bool
}
type rbacDriver struct{ state *rbacDBState }

func openRBACDatabase(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("chitu-rbac-%d", rbacDriverSequence.Add(1))
	sql.Register(name, rbacDriver{&rbacDBState{items: map[string]Item{}, children: map[string]map[string]bool{}, assignments: map[string]map[string]bool{}}})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return database
}
func (value rbacDriver) Open(string) (driver.Conn, error) {
	return &rbacConnection{state: value.state}, nil
}

type rbacConnection struct{ state *rbacDBState }

func (*rbacConnection) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (*rbacConnection) Close() error                        { return nil }
func (*rbacConnection) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (connection *rbacConnection) ExecContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	switch {
	case strings.HasPrefix(query, `INSERT INTO "app_auth_item" `):
		name := arguments[0].Value.(string)
		if _, exists := connection.state.items[name]; exists {
			return nil, errors.New("duplicate")
		}
		item := Item{Name: name, Kind: Kind(arguments[1].Value.(string))}
		if arguments[2].Value != nil {
			item.Description = arguments[2].Value.(string)
		}
		if arguments[3].Value != nil {
			item.Rule = arguments[3].Value.(string)
		}
		connection.state.items[name] = item
	case strings.HasPrefix(query, `DELETE FROM "app_auth_item"`):
		name := arguments[0].Value.(string)
		if _, exists := connection.state.items[name]; !exists {
			return driver.RowsAffected(0), nil
		}
		delete(connection.state.items, name)
		for _, children := range connection.state.children {
			delete(children, name)
		}
		for _, roles := range connection.state.assignments {
			delete(roles, name)
		}
	case strings.HasPrefix(query, `UPDATE "app_auth_item"`):
		name := arguments[3].Value.(string)
		item, exists := connection.state.items[name]
		if !exists {
			return driver.RowsAffected(0), nil
		}
		item.Kind = Kind(arguments[0].Value.(string))
		item.Description, item.Rule = "", ""
		if arguments[1].Value != nil {
			item.Description = arguments[1].Value.(string)
		}
		if arguments[2].Value != nil {
			item.Rule = arguments[2].Value.(string)
		}
		connection.state.items[name] = item
	case strings.HasPrefix(query, `INSERT INTO "app_auth_item_child"`):
		parent, child := arguments[0].Value.(string), arguments[1].Value.(string)
		if connection.state.children[parent] == nil {
			connection.state.children[parent] = map[string]bool{}
		}
		connection.state.children[parent][child] = true
	case strings.HasPrefix(query, `DELETE FROM "app_auth_item_child"`):
		parent, child := arguments[0].Value.(string), arguments[1].Value.(string)
		if !connection.state.children[parent][child] {
			return driver.RowsAffected(0), nil
		}
		delete(connection.state.children[parent], child)
	case strings.HasPrefix(query, `INSERT INTO "app_auth_assignment"`):
		user, role := arguments[0].Value.(string), arguments[1].Value.(string)
		if connection.state.assignments[user] == nil {
			connection.state.assignments[user] = map[string]bool{}
		}
		connection.state.assignments[user][role] = true
	case strings.HasPrefix(query, `DELETE FROM "app_auth_assignment"`):
		user, role := arguments[0].Value.(string), arguments[1].Value.(string)
		if !connection.state.assignments[user][role] {
			return driver.RowsAffected(0), nil
		}
		delete(connection.state.assignments[user], role)
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	return driver.RowsAffected(1), nil
}

func (connection *rbacConnection) QueryContext(_ context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()
	columns := []string{"name", "type", "description", "rule_name"}
	var values [][]driver.Value
	switch {
	case strings.Contains(query, " JOIN "):
		parent := arguments[0].Value.(string)
		for child := range connection.state.children[parent] {
			values = append(values, itemValues(connection.state.items[child]))
		}
	case strings.HasPrefix(query, `SELECT "user_id"`):
		columns = []string{"user_id", "role_name"}
		user := arguments[0].Value.(string)
		for role := range connection.state.assignments[user] {
			values = append(values, []driver.Value{user, role})
		}
	case strings.Contains(query, `WHERE "name"`):
		if item, found := connection.state.items[arguments[0].Value.(string)]; found {
			values = append(values, itemValues(item))
		}
	case strings.HasPrefix(query, `SELECT "name"`):
		for _, item := range connection.state.items {
			if len(arguments) == 0 || string(item.Kind) == arguments[0].Value.(string) {
				values = append(values, itemValues(item))
			}
		}
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	sort.Slice(values, func(i, j int) bool { return fmt.Sprint(values[i][0]) < fmt.Sprint(values[j][0]) })
	return &rbacRows{columns: columns, values: values}, nil
}
func itemValues(item Item) []driver.Value {
	var description, rule driver.Value
	if item.Description != "" {
		description = item.Description
	}
	if item.Rule != "" {
		rule = item.Rule
	}
	return []driver.Value{item.Name, string(item.Kind), description, rule}
}

type rbacRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *rbacRows) Columns() []string { return rows.columns }
func (*rbacRows) Close() error           { return nil }
func (rows *rbacRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

var _ driver.Driver = rbacDriver{}
var _ driver.ExecerContext = (*rbacConnection)(nil)
var _ driver.QueryerContext = (*rbacConnection)(nil)
