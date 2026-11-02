package gii

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/ar"
	"github.com/chitushka/framework/db/migration"
)

func sampleSchema() ar.TableSchema {
	return ar.TableSchema{Name: "public.users", Columns: []ar.ColumnSchema{{Name: "id", DataType: "BIGINT", PrimaryKey: true, AutoIncrement: true}, {Name: "email", DataType: "VARCHAR(255)"}, {Name: "age", DataType: "INTEGER"}, {Name: "created_at", DataType: "TIMESTAMP"}, {Name: "version", DataType: "INTEGER", Version: true}}}
}

func TestGenerateAndWriteCRUD(t *testing.T) {
	directory := t.TempDir()
	files, err := GenerateCRUD(sampleSchema(), CRUDOptions{Package: "controller", ModelPackage: "example.com/app/model", ModelType: "User", OutputDir: directory, Route: "/users"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d", len(files))
	}
	for _, file := range files {
		if strings.HasSuffix(file.Path, ".go") {
			if _, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Content, parser.AllErrors); err != nil {
				t.Fatalf("parse %s: %v\n%s", file.Path, err, file.Content)
			}
		}
	}
	controller := string(files[0].Content)
	for _, expected := range []string{"type UserController", "type UserInput", "RegisterUserRoutes", "repository.Insert", "repository.Update", "repository.Delete", "time.Time"} {
		if !strings.Contains(controller, expected) {
			t.Fatalf("controller missing %q:\n%s", expected, controller)
		}
	}
	if strings.Contains(controller, "ID *") || strings.Contains(controller, "Version *") {
		t.Fatal("protected fields included in input")
	}
	search := string(files[1].Content)
	if !strings.Contains(search, "type UserSearch") || !strings.Contains(search, `frameworkdb.Eq("email", value)`) {
		t.Fatalf("search = %s", search)
	}
	vue := string(files[2].Content)
	if !strings.Contains(vue, "<script setup>") || !strings.Contains(vue, "table table-hover") || !strings.Contains(vue, "method: creating ? 'POST' : 'PUT'") || !strings.Contains(vue, `v-model.number="form.age"`) {
		t.Fatalf("vue = %s", vue)
	}
	created, err := WriteCRUD(sampleSchema(), CRUDOptions{ModelPackage: "example.com/app/model", ModelType: "User", OutputDir: directory, VueOutput: filepath.Join(directory, "UserView.vue")})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 3 {
		t.Fatalf("created = %#v", created)
	}
	if _, err := os.Stat(created[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCRUD(sampleSchema(), CRUDOptions{ModelPackage: "example.com/app/model", ModelType: "User", OutputDir: directory, VueOutput: filepath.Join(directory, "UserView.vue")}); err == nil {
		t.Fatal("existing files overwritten")
	}
}

func TestGenerateMigrationSQLAndFiles(t *testing.T) {
	registry := frameworkdb.NewDefaultDialectRegistry()
	for _, name := range []string{frameworkdb.PostgreSQL, frameworkdb.MySQL, frameworkdb.Oracle, frameworkdb.SQLServer} {
		dialect, _ := registry.Require(name)
		up, down, err := GenerateMigrationSQL(sampleSchema(), dialect)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(up, "CREATE TABLE") || !strings.Contains(up, "PRIMARY KEY") || !strings.Contains(down, "DROP TABLE") {
			t.Fatalf("%s SQL: %s / %s", name, up, down)
		}
	}
	dialect, _ := registry.Require(frameworkdb.PostgreSQL)
	directory := t.TempDir()
	files, err := WriteMigration(directory, "create_users", time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), sampleSchema(), dialect)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files.Up); err != nil {
		t.Fatal(err)
	}
	migrations, err := (migration.DirectorySource{Path: directory}).Migrations(context.Background())
	if err != nil || len(migrations) != 1 {
		t.Fatalf("migrations = %#v, %v", migrations, err)
	}
}

func TestGeneratorValidation(t *testing.T) {
	valid := sampleSchema()
	tests := []struct {
		name    string
		schema  ar.TableSchema
		options CRUDOptions
	}{{"empty", ar.TableSchema{}, CRUDOptions{}}, {"package", valid, CRUDOptions{Package: "bad-package", ModelPackage: "x", OutputDir: "x"}}, {"model package", valid, CRUDOptions{OutputDir: "x"}}, {"output", valid, CRUDOptions{ModelPackage: "x"}}, {"route", valid, CRUDOptions{ModelPackage: "x", OutputDir: "x", Route: "relative"}}, {"primary", ar.TableSchema{Name: "users", Columns: []ar.ColumnSchema{{Name: "email"}}}, CRUDOptions{ModelPackage: "x", OutputDir: "x"}}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := GenerateCRUD(test.schema, test.options); err == nil {
				t.Fatal("validation error = nil")
			}
		})
	}
	if _, _, err := GenerateMigrationSQL(ar.TableSchema{}, nil); err == nil {
		t.Fatal("nil dialect accepted")
	}
	dialect, _ := frameworkdb.NewDefaultDialectRegistry().Require(frameworkdb.PostgreSQL)
	unsafe := sampleSchema()
	unsafe.Columns[0].DataType = "BIGINT; DROP TABLE users"
	if _, _, err := GenerateMigrationSQL(unsafe, dialect); err == nil {
		t.Fatal("unsafe SQL type accepted")
	}
}
