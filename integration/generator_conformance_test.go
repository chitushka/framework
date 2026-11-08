package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/ar"
	"github.com/chitushka/framework/gii"
)

func TestGeneratedProjectsFromBaselineSchemas(t *testing.T) {
	sqlBaseline(t, func(t *testing.T, c *db.Connection) {
		ctx := testContext(t)
		id := "BIGSERIAL PRIMARY KEY"
		if c.Dialect().Name() == db.MySQL {
			id = "BIGINT AUTO_INCREMENT PRIMARY KEY"
		}
		table := sqlTable(t, c, "id "+id+", name VARCHAR(255) NOT NULL, optional VARCHAR(100), price DECIMAL(20,6) NOT NULL, version BIGINT NOT NULL, created_at TIMESTAMP NOT NULL")
		inspector := ar.DatabaseInspector{Connection: c, Tables: map[string]ar.InspectOptions{table: {PrimaryKeys: []string{"id"}, AutoIncrement: "id", VersionColumn: "version"}}}
		schema, err := inspector.Inspect(ctx, table)
		check(t, err)
		root := t.TempDir()
		moduleRoot, err := filepath.Abs("..")
		check(t, err)
		check(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte(fmt.Sprintf("module generated.test/app\n\ngo 1.26.10\nrequire github.com/chitushka/framework v0.0.0\nreplace github.com/chitushka/framework => %s\n", filepath.ToSlash(moduleRoot))), 0o600))
		modelPath := filepath.Join(root, "model", "record.go")
		generator := ar.Generator{Inspector: inspector}
		check(t, generator.GenerateFile(ctx, table, ar.GenerateOptions{Package: "model", TypeName: "Record", Output: modelPath}))
		if err := generator.GenerateFile(ctx, table, ar.GenerateOptions{Output: modelPath}); err == nil {
			t.Fatal("model overwrite accepted")
		}
		files, err := gii.GenerateCRUD(schema, gii.CRUDOptions{Package: "controller", ModelPackage: "generated.test/app/model", ModelType: "Record", OutputDir: filepath.Join(root, "controller")})
		check(t, err)
		changes, err := gii.Preview(files)
		check(t, err)
		if len(changes) != 3 {
			t.Fatal("missing preview files")
		}
		if _, err := os.Stat(filepath.Join(root, "controller")); !os.IsNotExist(err) {
			t.Fatal("preview wrote output")
		}
		_, err = gii.WriteFiles(files)
		check(t, err)
		if _, err := gii.WriteFiles(files); err == nil {
			t.Fatal("CRUD overwrite accepted")
		}
		modelTest := `package model
import ("encoding/json";"testing")
func TestGeneratedModel(t *testing.T){var m Record;if err:=json.Unmarshal([]byte("{\"id\":7,\"name\":\"name\",\"price\":\"123456789012.123456\",\"optional\":null}"),&m);err!=nil{t.Fatal(err)};if m.ID!=7||m.Price!="123456789012.123456"||m.Optional!=nil{t.Fatalf("bad generated model: %+v",m)}}`
		check(t, os.WriteFile(filepath.Join(root, "model", "record_test.go"), []byte(modelTest), 0o600))
		crudTest := `package controller
import("testing";"generated.test/app/model")
func TestInputExcludesIdentityAndPreservesMissingFields(t *testing.T){name:="updated";m:=model.Record{ID:7,Name:"old",Price:"1.500000",Version:3};input:=RecordInput{Name:&name};input.Apply(&m);if m.ID!=7||m.Version!=3||m.Name!="updated"||m.Price!="1.500000"{t.Fatalf("mass assignment: %+v",m)}}`
		check(t, os.WriteFile(filepath.Join(root, "controller", "record_test.go"), []byte(crudTest), 0o600))
		command := exec.CommandContext(ctx, "go", "test", "-mod=mod", "./...")
		command.Dir = root
		command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generated project failed: %v\n%s", err, out)
		}
		copySchema := schema
		copySchema.Name = unique()
		up, down, err := gii.GenerateMigrationSQL(copySchema, c.Dialect())
		check(t, err)
		_, err = c.ExecContext(ctx, up)
		check(t, err)
		_, err = c.ExecContext(ctx, down)
		check(t, err)
		paths, err := gii.WriteMigration(filepath.Join(root, "migrations"), "copy", time.Now(), copySchema, c.Dialect())
		check(t, err)
		if _, err := os.Stat(paths.Up); err != nil {
			t.Fatal(err)
		}
	})
}
