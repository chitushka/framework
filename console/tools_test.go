package console

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

func TestRegisteredToolsGenerateModelCRUDAndMigration(t *testing.T) {
	driverName := registerToolDriver()
	open := func(ctx context.Context, config frameworkdb.Config) (*frameworkdb.Connection, error) {
		return frameworkdb.Open(ctx, config, frameworkdb.NewDefaultDialectRegistry())
	}
	now := func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
	root := t.TempDir()
	modelPath := filepath.Join(root, "model", "user.go")
	runTool(t, open, now, []string{"generate:model", "-driver", driverName, "-dialect", frameworkdb.PostgreSQL, "-pk", "id", "-auto", "id", "-output", modelPath, "users"})
	model, err := os.ReadFile(modelPath)
	if err != nil || !strings.Contains(string(model), "type User struct") {
		t.Fatalf("model = %s, %v", model, err)
	}
	crudDir := filepath.Join(root, "controller")
	runTool(t, open, now, []string{"generate:crud", "-driver", driverName, "-dialect", frameworkdb.PostgreSQL, "-pk", "id", "-auto", "id", "-output", crudDir, "-model-package", "example.com/app/model", "users"})
	for _, name := range []string{"user_controller.go", "user_search.go", "user.vue"} {
		if _, err := os.Stat(filepath.Join(crudDir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	migrationDir := filepath.Join(root, "migrations")
	output := runTool(t, open, now, []string{"generate:migration", "-driver", driverName, "-dialect", frameworkdb.PostgreSQL, "-pk", "id", "-auto", "id", "-dir", migrationDir, "users"})
	if !strings.Contains(output, "created") {
		t.Fatalf("output = %q", output)
	}
	up, err := os.ReadFile(filepath.Join(migrationDir, "20261102120000_create_users.up.sql"))
	if err != nil || !strings.Contains(string(up), "CREATE TABLE") {
		t.Fatalf("migration = %s, %v", up, err)
	}
}

func TestMigrationCreateAndToolValidation(t *testing.T) {
	directory := t.TempDir()
	output := runTool(t, nil, func() time.Time { return time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC) }, []string{"migration:create", "-dir", directory, "add-users"})
	if !strings.Contains(output, "created") {
		t.Fatalf("output = %q", output)
	}
	application := New(nil)
	if err := RegisterTools(application, ToolOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterTools(application, ToolOptions{}); err == nil {
		t.Fatal("duplicate tools accepted")
	}
	if err := RegisterTools(nil, ToolOptions{}); err == nil {
		t.Fatal("nil application accepted")
	}
}

func runTool(t *testing.T, open OpenFunc, now func() time.Time, args []string) string {
	t.Helper()
	application := New(nil)
	var output bytes.Buffer
	application.Out = &output
	application.Err = &output
	if err := RegisterTools(application, ToolOptions{Open: open, Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := application.Run(context.Background(), args); err != nil {
		t.Fatalf("Run(%q): %v\n%s", args, err, output.String())
	}
	return output.String()
}

var toolDriverSequence atomic.Uint64

func registerToolDriver() string {
	name := fmt.Sprintf("chitu-console-tool-%d", toolDriverSequence.Add(1))
	sql.Register(name, toolDriver{})
	return name
}

type toolDriver struct{}

func (toolDriver) Open(string) (driver.Conn, error) { return toolConnection{}, nil }

type toolConnection struct{}

func (toolConnection) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (toolConnection) Close() error                        { return nil }
func (toolConnection) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }
func (toolConnection) Ping(context.Context) error          { return nil }
func (toolConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &toolRows{columns: []string{"id", "email", "created_at"}, types: []string{"BIGINT", "VARCHAR(255)", "TIMESTAMP"}, nullable: []bool{false, false, false}}, nil
}

type toolRows struct {
	columns, types []string
	nullable       []bool
}

func (rows *toolRows) Columns() []string                           { return rows.columns }
func (*toolRows) Close() error                                     { return nil }
func (*toolRows) Next([]driver.Value) error                        { return io.EOF }
func (rows *toolRows) ColumnTypeDatabaseTypeName(index int) string { return rows.types[index] }
func (rows *toolRows) ColumnTypeNullable(index int) (bool, bool)   { return rows.nullable[index], true }

var _ driver.Driver = toolDriver{}
var _ driver.Pinger = toolConnection{}
var _ driver.QueryerContext = toolConnection{}
var _ driver.RowsColumnTypeDatabaseTypeName = (*toolRows)(nil)
var _ driver.RowsColumnTypeNullable = (*toolRows)(nil)
