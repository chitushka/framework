# Generator quickstart без SQL-сервера

## Создать model, CRUD и migration

Нужен только Go из `go.mod`. Из корня checkout:

```bash
go mod download
go run ./examples/guide/generate -output .cache/guide-catalog
go test ./examples/guide/generate
```

Команда создаёт `model/product.go`, controller/search/input Go files,
`frontend/ProductView.vue` и пару `migrations/*.up.sql`/`*.down.sql` для PostgreSQL.
ModelPackage в примере — `example.com/catalog/model`; при адаптации к своему module
измените import path. Price — decimal string, ID auto primary key, version —
optimistic lock; input не включает ID/version. Схема задана явно, без SQL driver.

Повтор в тот же output отвергается без overwrite. Для нового прогона укажите новый
каталог; не удаляйте существующий application code ради повторной генерации.
Generator фиксирует timestamp учебной migration для детерминированного результата.
Для реальных последовательных миграций задавайте уникальный timestamp/имя.

```go file=../../examples/guide/generate/main.go
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
```

## Компиляция как нового проекта

Из Linux/macOS shell, после генерации выше, запомните абсолютный checkout path:

```bash
framework_root="$PWD"
cd .cache/guide-catalog
go mod init example.com/catalog
go mod edit -require=github.com/chitushka/framework@v0.0.0
go mod edit "-replace=github.com/chitushka/framework=$framework_root"
go mod tidy
go test ./...
```

В PowerShell перед `cd` задайте `$frameworkRoot = (Get-Location).Path`, затем
`go mod edit "-replace=github.com/chitushka/framework=$frameworkRoot"`; остальные
Go-команды одинаковы. `v0.0.0` здесь только placeholder для local replace, не
опубликованный framework release. Внешний проект использует именно ваш checkout;
перед поставкой закрепите принятый version tag по [support policy](support.md).

Generated controller ещё не подключён к HTTP server/DB/authorization: это scaffold.
Регистрируйте routes, connection и access policy приложения, а Vue view подключайте
к своему Vite проекту; отдельный `.vue` не становится готовой browser-сборкой.

`go run ./tools/checkdocs -run` делает эту проверку в новом временном module, без
существующего output или `go.work`, и дополнительно запускает generated input-policy
test. PostgreSQL DDL проверяется как текст; настоящий DDL и DatabaseInspector обеих
baseline СУБД уже проверяет [real-service suite](../../integration/generator_conformance_test.go).
Дальше используйте [generators contract](generators.md) для preview/no-overwrite
и [migration recipe](cookbook/migrations.md) для применения SQL.
