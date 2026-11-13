// Command generate creates a model, CRUD, Vue scaffold and PostgreSQL migration.
package main

import (
	"flag"
	"log"
	"path/filepath"
	"time"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/ar"
	"github.com/chitushka/framework/gii"
)

func generate(directory string) error {
	schema := ar.TableSchema{Name: "products", Columns: []ar.ColumnSchema{
		{Name: "id", GoType: "int64", PrimaryKey: true, AutoIncrement: true},
		{Name: "name", GoType: "string"},
		{Name: "price", DataType: "DECIMAL(12,2)", GoType: "string"},
		{Name: "version", GoType: "int64", Version: true},
	}}
	model, err := ar.GenerateModel(schema, ar.GenerateOptions{Package: "model", TypeName: "Product"})
	if err != nil {
		return err
	}
	files, err := gii.GenerateCRUD(schema, gii.CRUDOptions{
		Package: "controller", ModelPackage: "example.com/catalog/model", ModelType: "Product",
		OutputDir: filepath.Join(directory, "controller"),
		VueOutput: filepath.Join(directory, "frontend", "ProductView.vue"), Route: "/products",
	})
	if err != nil {
		return err
	}
	files = append(files, gii.GeneratedFile{Path: filepath.Join(directory, "model", "product.go"), Content: model})
	if _, err := gii.WriteFiles(files); err != nil {
		return err
	}
	dialect, err := db.NewDefaultDialectRegistry().Require(db.PostgreSQL)
	if err != nil {
		return err
	}
	_, err = gii.WriteMigration(filepath.Join(directory, "migrations"), "create_products", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), schema, dialect)
	return err
}

func main() {
	directory := flag.String("output", ".cache/guide-catalog", "new output directory; existing files are rejected")
	flag.Parse()
	if err := generate(*directory); err != nil {
		log.Fatal(err)
	}
}
