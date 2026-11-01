package ar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateModel(t *testing.T) {
	schema := TableSchema{
		Name: "public.users",
		Columns: []ColumnSchema{
			{Name: "id", DataType: "bigint", PrimaryKey: true, AutoIncrement: true},
			{Name: "email", DataType: "varchar"},
			{Name: "created_at", DataType: "timestamp"},
			{Name: "deleted_at", DataType: "timestamp", Nullable: true},
			{Name: "version", DataType: "integer", Version: true},
		},
		Relations: []RelationSchema{{
			Name: "Posts", ModelType: "Post", LocalColumn: "id", ForeignColumn: "user_id", Many: true,
		}},
	}
	source, err := GenerateModel(schema, GenerateOptions{Package: "model"})
	if err != nil {
		t.Fatalf("GenerateModel() error = %v", err)
	}
	text := string(source)
	for _, expected := range []string{
		"type User struct",
		"ID        int64",
		"`db:\"id,primary,auto\" json:\"id\"`",
		"DeletedAt *time.Time",
		`func (User) TableName() string { return "public.users" }`,
		"func (model *User) Posts",
		"return ar.HasMany",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated source does not contain %q:\n%s", expected, text)
		}
	}
}

func TestGeneratorUsesInspectorAndDoesNotOverwrite(t *testing.T) {
	inspector := &testInspector{schema: TableSchema{
		Name: "accounts", Columns: []ColumnSchema{{Name: "id", GoType: "int64", PrimaryKey: true}},
	}}
	path := filepath.Join(t.TempDir(), "model", "account.go")
	generator := Generator{Inspector: inspector}
	if err := generator.GenerateFile(context.Background(), "accounts", GenerateOptions{Package: "model", Output: path}); err != nil {
		t.Fatalf("GenerateFile() error = %v", err)
	}
	if inspector.table != "accounts" {
		t.Fatalf("Inspect() table = %q", inspector.table)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("generated file: %v", err)
	}
	if err := generator.GenerateFile(context.Background(), "accounts", GenerateOptions{Package: "model", Output: path}); err == nil {
		t.Fatal("second GenerateFile() error = nil")
	}
}

func TestGenerateModelValidation(t *testing.T) {
	if _, err := GenerateModel(TableSchema{Name: "users"}, GenerateOptions{}); err == nil {
		t.Fatal("empty schema error = nil")
	}
	if _, err := (Generator{}).Generate(context.Background(), "users", GenerateOptions{}); err == nil {
		t.Fatal("nil inspector error = nil")
	}
	tests := []struct {
		name    string
		schema  TableSchema
		options GenerateOptions
	}{
		{"invalid table", TableSchema{Name: "bad table", Columns: []ColumnSchema{{Name: "id"}}}, GenerateOptions{}},
		{"invalid package", TableSchema{Name: "users", Columns: []ColumnSchema{{Name: "id"}}}, GenerateOptions{Package: "bad-package"}},
		{"invalid type", TableSchema{Name: "users", Columns: []ColumnSchema{{Name: "id"}}}, GenerateOptions{TypeName: "user"}},
		{"empty column", TableSchema{Name: "users", Columns: []ColumnSchema{{Name: ""}}}, GenerateOptions{}},
		{"invalid relation", TableSchema{Name: "users", Columns: []ColumnSchema{{Name: "id"}}, Relations: []RelationSchema{{Name: "bad", ModelType: "Post"}}}, GenerateOptions{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := GenerateModel(tt.schema, tt.options); err == nil {
				t.Fatal("GenerateModel() error = nil")
			}
		})
	}
	failing := Generator{Inspector: errorInspector{}}
	if _, err := failing.Generate(context.Background(), "users", GenerateOptions{}); err == nil {
		t.Fatal("inspector error was lost")
	}
	if err := (Generator{Inspector: &testInspector{schema: TableSchema{Name: "users", Columns: []ColumnSchema{{Name: "id"}}}}}).GenerateFile(
		context.Background(), "users", GenerateOptions{},
	); err == nil {
		t.Fatal("empty output error = nil")
	}
}

func TestDatabaseInspectorAndGenerator(t *testing.T) {
	connection, recorder := openARConnection(t)
	defer connection.Close()
	recorder.pushQuery(arQueryResponse{
		columns:  []string{"id", "email", "version"},
		types:    []string{"BIGINT", "VARCHAR", "INTEGER"},
		nullable: []bool{false, true, false},
	})
	inspector := DatabaseInspector{
		Connection: connection,
		Tables: map[string]InspectOptions{"users": {
			PrimaryKeys: []string{"id"}, AutoIncrement: "id", VersionColumn: "version",
		}},
	}
	source, err := (Generator{Inspector: inspector}).Generate(context.Background(), "users", GenerateOptions{})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	text := string(source)
	if !strings.Contains(text, `db:"id,primary,auto"`) || !strings.Contains(text, `*string `+"`"+`db:"email"`) || !strings.Contains(text, `db:"version,version"`) {
		t.Fatalf("generated inspected model:\n%s", text)
	}
	if call := recorder.queryCall(0); call.query != `SELECT * FROM "users" WHERE 1 = 0` {
		t.Fatalf("inspector query = %q", call.query)
	}
	if _, err := (DatabaseInspector{}).Inspect(context.Background(), "users"); err == nil {
		t.Fatal("nil inspector connection error = nil")
	}
	if _, err := (DatabaseInspector{Connection: connection}).Inspect(context.Background(), "bad table"); err == nil {
		t.Fatal("invalid inspector table error = nil")
	}
}

func TestGeneratorTypeAndNameHelpers(t *testing.T) {
	tests := map[string]string{
		"BOOLEAN": "bool", "BIT": "bool", "BIGINT": "int64", "SERIAL": "int64",
		"NUMERIC": "float64", "DOUBLE PRECISION": "float64", "TIMESTAMP": "time.Time",
		"BLOB": "[]byte", "BYTEA": "[]byte", "VARCHAR": "string",
	}
	for databaseType, want := range tests {
		if got := databaseGoType(databaseType); got != want {
			t.Fatalf("databaseGoType(%q) = %q, want %q", databaseType, got, want)
		}
	}
	if exportedName("user_id") != "UserID" || singular("categories") != "category" || singular("users") != "user" || singular("status") != "status" {
		t.Fatal("name helper result changed")
	}
}

type testInspector struct {
	schema TableSchema
	table  string
}

type errorInspector struct{}

func (errorInspector) Inspect(context.Context, string) (TableSchema, error) {
	return TableSchema{}, errors.New("inspect failed")
}

func (inspector *testInspector) Inspect(_ context.Context, table string) (TableSchema, error) {
	inspector.table = table
	return inspector.schema, nil
}
