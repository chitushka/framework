package console

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
	"github.com/chitushka/framework/db/ar"
	"github.com/chitushka/framework/db/migration"
	"github.com/chitushka/framework/gii"
)

type OpenFunc func(context.Context, frameworkdb.Config) (*frameworkdb.Connection, error)
type ToolOptions struct {
	Open OpenFunc
	Now  func() time.Time
}

// RegisterTools installs migration and Gii-style model/CRUD commands.
func RegisterTools(application *Application, options ToolOptions) error {
	if application == nil {
		return errors.New("console: application is nil")
	}
	if options.Open == nil {
		options.Open = func(ctx context.Context, config frameworkdb.Config) (*frameworkdb.Connection, error) {
			return frameworkdb.Open(ctx, config, frameworkdb.NewDefaultDialectRegistry())
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	commands := []Command{
		migrationCommand("migration:create", "create an empty up/down migration", options), migrationCommand("migration:up", "apply pending migrations", options), migrationCommand("migration:down", "revert applied migrations", options), migrationCommand("migration:status", "show migration status", options), migrationCommand("migration:resolve", "resolve inspected dirty migration state", options),
		CommandFunc{CommandName: "generate:migration", CommandDescription: "generate a create-table migration from database metadata", Execute: func(ctx context.Context, console *Context, args []string) error {
			return runMigrationGenerator(ctx, console, args, options)
		}},
		CommandFunc{CommandName: "generate:model", CommandDescription: "generate an Active Record model from a database table", Execute: func(ctx context.Context, console *Context, args []string) error {
			return runModelGenerator(ctx, console, args, options)
		}},
		CommandFunc{CommandName: "generate:crud", CommandDescription: "generate CRUD controller, search model, and Vue component", Execute: func(ctx context.Context, console *Context, args []string) error {
			return runCRUDGenerator(ctx, console, args, options)
		}},
	}
	return application.Register(commands...)
}

func migrationCommand(name, description string, options ToolOptions) Command {
	return CommandFunc{CommandName: name, CommandDescription: description, Execute: func(ctx context.Context, console *Context, args []string) error {
		cli := migration.NewCLI()
		cli.Out = console.Out
		cli.Err = console.Err
		cli.Now = options.Now
		cli.Open = func(ctx context.Context, config frameworkdb.Config) (*frameworkdb.Connection, error) {
			return options.Open(ctx, config)
		}
		return cli.Run(ctx, append([]string{name}, args...))
	}}
}

func runMigrationGenerator(ctx context.Context, console *Context, args []string, options ToolOptions) error {
	flags := flag.NewFlagSet("generate:migration", flag.ContinueOnError)
	flags.SetOutput(console.Err)
	values := generatorFlags{}
	flags.StringVar(&values.driver, "driver", os.Getenv("DB_DRIVER"), "database/sql driver")
	flags.StringVar(&values.dsn, "dsn", os.Getenv("DB_DSN"), "database DSN")
	flags.StringVar(&values.dialect, "dialect", os.Getenv("DB_DIALECT"), "SQL dialect")
	flags.StringVar(&values.primary, "pk", "", "comma-separated primary key columns")
	flags.StringVar(&values.auto, "auto", "", "auto-increment column")
	directory := flags.String("dir", envOr("MIGRATION_DIR", "migrations"), "migration directory")
	name := flags.String("name", "", "migration name")
	dryRun := flags.Bool("dry-run", false, "preview without writing")
	diff := flags.Bool("diff", false, "show diff without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("console: generate:migration requires exactly one table")
	}
	table := flags.Arg(0)
	schema, closeConnection, err := inspectSchema(ctx, values, table, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	dialect, err := frameworkdb.NewDefaultDialectRegistry().Require(values.dialect)
	if err != nil {
		return err
	}
	migrationName := *name
	if migrationName == "" {
		migrationName = "create_" + strings.ReplaceAll(table, ".", "_")
	}
	if *dryRun || *diff {
		up, down, err := gii.GenerateMigrationSQL(schema, dialect)
		if err != nil {
			return err
		}
		paths, err := migration.Plan(*directory, migrationName, options.Now())
		if err != nil {
			return err
		}
		return printPreview(console.Out, []gii.GeneratedFile{{Path: paths.Up, Content: []byte(up)}, {Path: paths.Down, Content: []byte(down)}}, *diff)
	}
	files, err := gii.WriteMigration(*directory, migrationName, options.Now(), schema, dialect)
	if err != nil {
		return err
	}
	fmt.Fprintf(console.Out, "created %s\ncreated %s\n", files.Up, files.Down)
	return nil
}

type generatorFlags struct {
	driver, dsn, dialect, packageName, typeName, output, primary, auto, version string
	dryRun, diff                                                                bool
}

func parseGeneratorFlags(name string, args []string, errOutput interface{ Write([]byte) (int, error) }) (generatorFlags, string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errOutput)
	values := generatorFlags{}
	flags.StringVar(&values.driver, "driver", os.Getenv("DB_DRIVER"), "database/sql driver")
	flags.StringVar(&values.dsn, "dsn", os.Getenv("DB_DSN"), "database DSN")
	flags.StringVar(&values.dialect, "dialect", os.Getenv("DB_DIALECT"), "SQL dialect")
	flags.StringVar(&values.packageName, "package", "model", "generated Go package")
	flags.StringVar(&values.typeName, "type", "", "generated model type")
	flags.StringVar(&values.output, "output", "", "output path or directory")
	flags.StringVar(&values.primary, "pk", "", "comma-separated primary key columns")
	flags.StringVar(&values.auto, "auto", "", "auto-increment column")
	flags.StringVar(&values.version, "version", "", "optimistic lock column")
	flags.BoolVar(&values.dryRun, "dry-run", false, "preview without writing")
	flags.BoolVar(&values.diff, "diff", false, "show diff without writing")
	if err := flags.Parse(args); err != nil {
		return generatorFlags{}, "", err
	}
	if flags.NArg() != 1 {
		return generatorFlags{}, "", fmt.Errorf("console: %s requires exactly one table", name)
	}
	return values, flags.Arg(0), nil
}
func inspectSchema(ctx context.Context, values generatorFlags, table string, options ToolOptions) (ar.TableSchema, func(), error) {
	connection, err := options.Open(ctx, frameworkdb.Config{Driver: values.driver, DSN: values.dsn, Dialect: values.dialect})
	if err != nil {
		return ar.TableSchema{}, nil, err
	}
	closeConnection := func() { _ = connection.Close() }
	inspector := ar.DatabaseInspector{Connection: connection, Tables: map[string]ar.InspectOptions{table: {PrimaryKeys: splitList(values.primary), AutoIncrement: values.auto, VersionColumn: values.version}}}
	schema, err := inspector.Inspect(ctx, table)
	if err != nil {
		closeConnection()
		return ar.TableSchema{}, nil, err
	}
	return schema, closeConnection, nil
}
func runModelGenerator(ctx context.Context, console *Context, args []string, options ToolOptions) error {
	values, table, err := parseGeneratorFlags("generate:model", args, console.Err)
	if err != nil {
		return err
	}
	if values.output == "" {
		return errors.New("console: generate:model requires -output")
	}
	schema, closeConnection, err := inspectSchema(ctx, values, table, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	source, err := ar.GenerateModel(schema, ar.GenerateOptions{Package: values.packageName, TypeName: values.typeName})
	if err != nil {
		return err
	}
	generator := ar.Generator{Inspector: staticInspector{schema: schema}}
	if values.dryRun || values.diff {
		return printPreview(console.Out, []gii.GeneratedFile{{Path: values.output, Content: source}}, values.diff)
	}
	if err := generator.GenerateFile(ctx, table, ar.GenerateOptions{Package: values.packageName, TypeName: values.typeName, Output: values.output}); err != nil {
		return err
	}
	fmt.Fprintf(console.Out, "created %s (%d bytes)\n", values.output, len(source))
	return nil
}

type staticInspector struct{ schema ar.TableSchema }

func (inspector staticInspector) Inspect(context.Context, string) (ar.TableSchema, error) {
	return inspector.schema, nil
}

func runCRUDGenerator(ctx context.Context, console *Context, args []string, options ToolOptions) error {
	flags := flag.NewFlagSet("generate:crud", flag.ContinueOnError)
	flags.SetOutput(console.Err)
	values := generatorFlags{}
	flags.StringVar(&values.driver, "driver", os.Getenv("DB_DRIVER"), "database/sql driver")
	flags.StringVar(&values.dsn, "dsn", os.Getenv("DB_DSN"), "database DSN")
	flags.StringVar(&values.dialect, "dialect", os.Getenv("DB_DIALECT"), "SQL dialect")
	flags.StringVar(&values.primary, "pk", "", "comma-separated primary key columns")
	flags.StringVar(&values.auto, "auto", "", "auto-increment column")
	flags.StringVar(&values.version, "version", "", "optimistic lock column")
	controllerPackage := flags.String("package", "controller", "controller package")
	modelPackage := flags.String("model-package", "", "model import path")
	modelAlias := flags.String("model-alias", "", "model import alias")
	modelType := flags.String("type", "", "model type")
	output := flags.String("output", "", "controller output directory")
	vueOutput := flags.String("vue-output", "", "Vue component output path")
	route := flags.String("route", "", "route prefix")
	dryRun := flags.Bool("dry-run", false, "preview without writing")
	diff := flags.Bool("diff", false, "show diff without writing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("console: generate:crud requires exactly one table")
	}
	if *output == "" || *modelPackage == "" {
		return errors.New("console: generate:crud requires -output and -model-package")
	}
	table := flags.Arg(0)
	schema, closeConnection, err := inspectSchema(ctx, values, table, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	files, err := gii.GenerateCRUD(schema, gii.CRUDOptions{Package: *controllerPackage, ModelPackage: *modelPackage, ModelAlias: *modelAlias, ModelType: *modelType, OutputDir: *output, VueOutput: *vueOutput, Route: *route})
	if err != nil {
		return err
	}
	if *dryRun || *diff {
		return printPreview(console.Out, files, *diff)
	}
	created, err := gii.WriteFiles(files)
	if err != nil {
		return err
	}
	for _, path := range created {
		fmt.Fprintf(console.Out, "created %s\n", path)
	}
	return nil
}

func printPreview(out io.Writer, files []gii.GeneratedFile, diff bool) error {
	changes, err := gii.Preview(files)
	if err != nil {
		return err
	}
	for _, change := range changes {
		if diff {
			_, err = io.WriteString(out, change.Diff)
		} else {
			_, err = fmt.Fprintf(out, "%s %s\n", change.State, change.Path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
