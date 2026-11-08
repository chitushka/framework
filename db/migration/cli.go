package migration

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	frameworkdb "github.com/chitushka/framework/db"
)

// OpenFunc lets an application register its database driver with a blank
// import while reusing all migration CLI commands.
type OpenFunc func(context.Context, frameworkdb.Config) (*frameworkdb.Connection, error)

// CLI implements create/up/down/status and explicit dirty recovery.
type CLI struct {
	Out  io.Writer
	Err  io.Writer
	Open OpenFunc
	Now  func() time.Time
}

func NewCLI() *CLI {
	return &CLI{
		Out: os.Stdout,
		Err: os.Stderr,
		Open: func(ctx context.Context, config frameworkdb.Config) (*frameworkdb.Connection, error) {
			return frameworkdb.Open(ctx, config, frameworkdb.NewDefaultDialectRegistry())
		},
		Now: time.Now,
	}
}

// Run executes one command. It returns errors instead of terminating the process.
func (cli *CLI) Run(ctx context.Context, args []string) error {
	if cli == nil {
		return errors.New("migration: CLI must not be nil")
	}
	if len(args) == 0 {
		return errors.New("migration: expected command: create, up, down, status, or resolve")
	}
	if cli.Out == nil {
		cli.Out = io.Discard
	}
	if cli.Err == nil {
		cli.Err = io.Discard
	}
	if cli.Now == nil {
		cli.Now = time.Now
	}
	switch args[0] {
	case "create", "migration:create":
		return cli.create(args[1:])
	case "up", "migration:up":
		return cli.up(ctx, args[1:])
	case "down", "migration:down":
		return cli.down(ctx, args[1:])
	case "status", "migration:status":
		return cli.status(ctx, args[1:])
	case "resolve", "migration:resolve":
		return cli.resolve(ctx, args[1:])
	default:
		return fmt.Errorf("migration: unknown command %q", args[0])
	}
}

func (cli *CLI) create(args []string) error {
	flags := flag.NewFlagSet("migration:create", flag.ContinueOnError)
	flags.SetOutput(cli.Err)
	directory := flags.String("dir", envOr("MIGRATION_DIR", "migrations"), "migration directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("migration: create requires exactly one name")
	}
	files, err := Create(*directory, flags.Arg(0), cli.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(cli.Out, "created %s\ncreated %s\n", files.Up, files.Down)
	return nil
}

func (cli *CLI) up(ctx context.Context, args []string) error {
	options, rest, err := cli.parseDatabaseFlags("migration:up", args, true)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("migration: up does not accept positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	runner, closeConnection, err := cli.runner(ctx, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	results, err := runner.Up(ctx, options.limit)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Fprintf(cli.Out, "applied %s (%s)\n", result.ID, result.Duration.Round(time.Millisecond))
	}
	if len(results) == 0 {
		fmt.Fprintln(cli.Out, "no pending migrations")
	}
	return nil
}

func (cli *CLI) down(ctx context.Context, args []string) error {
	options, rest, err := cli.parseDatabaseFlags("migration:down", args, true)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("migration: down does not accept positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	if !options.limitSet {
		options.limit = 1
	}
	runner, closeConnection, err := cli.runner(ctx, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	results, err := runner.Down(ctx, options.limit)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Fprintf(cli.Out, "reverted %s (%s)\n", result.ID, result.Duration.Round(time.Millisecond))
	}
	if len(results) == 0 {
		fmt.Fprintln(cli.Out, "no applied migrations")
	}
	return nil
}

func (cli *CLI) status(ctx context.Context, args []string) error {
	options, rest, err := cli.parseDatabaseFlags("migration:status", args, false)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("migration: status does not accept positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	runner, closeConnection, err := cli.runner(ctx, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	statuses, err := runner.Status(ctx)
	if err != nil {
		return err
	}
	writer := tabwriter.NewWriter(cli.Out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "STATE\tMIGRATION\tAPPLIED AT\tCHECKSUM\tDIRECTION\tSTARTED AT")
	for _, status := range statuses {
		appliedAt := "-"
		if !status.AppliedAt.IsZero() {
			appliedAt = status.AppliedAt.UTC().Format(time.RFC3339)
		}
		startedAt := "-"
		if !status.StartedAt.IsZero() {
			startedAt = status.StartedAt.UTC().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", status.State, status.ID, appliedAt, status.Checksum, status.Direction, startedAt)
	}
	return writer.Flush()
}

func (cli *CLI) resolve(ctx context.Context, args []string) error {
	options, rest, err := cli.parseDatabaseFlags("migration:resolve", args, false)
	if err != nil {
		return err
	}
	if len(rest) != 0 || options.id == "" || options.checksum == "" || !options.appliedSet {
		return errors.New("migration: resolve requires -id, -checksum and explicit -applied=true|false")
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	runner, closeConnection, err := cli.runner(ctx, options)
	if err != nil {
		return err
	}
	defer closeConnection()
	if err := runner.Resolve(ctx, options.id, options.checksum, options.applied); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cli.Out, "resolved %s (applied=%t)\n", options.id, options.applied)
	return err
}

type cliOptions struct {
	directory                            string
	table                                string
	driver                               string
	dsn                                  string
	dialect                              string
	limit                                int
	limitSet                             bool
	timeout, lockTimeout, releaseTimeout time.Duration
	id, checksum                         string
	applied, appliedSet                  bool
}

func (cli *CLI) parseDatabaseFlags(name string, args []string, withLimit bool) (cliOptions, []string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(cli.Err)
	options := cliOptions{}
	flags.StringVar(&options.directory, "dir", envOr("MIGRATION_DIR", "migrations"), "migration directory")
	flags.StringVar(&options.table, "table", envOr("MIGRATION_TABLE", DefaultTable), "history table")
	flags.StringVar(&options.driver, "driver", os.Getenv("DB_DRIVER"), "database/sql driver")
	flags.StringVar(&options.dsn, "dsn", os.Getenv("DB_DSN"), "database DSN")
	flags.StringVar(&options.dialect, "dialect", os.Getenv("DB_DIALECT"), "SQL dialect")
	flags.DurationVar(&options.timeout, "timeout", 5*time.Minute, "overall operation timeout")
	flags.DurationVar(&options.lockTimeout, "lock-timeout", 30*time.Second, "connection/lock acquisition timeout")
	flags.DurationVar(&options.releaseTimeout, "release-timeout", 5*time.Second, "lock cleanup timeout")
	if name == "migration:resolve" {
		flags.StringVar(&options.id, "id", "", "dirty migration ID")
		flags.StringVar(&options.checksum, "checksum", "", "exact dirty checksum from status/error")
		flags.Func("applied", "operator-verified final state: true or false", func(raw string) error {
			if raw != "true" && raw != "false" {
				return errors.New("applied must be true or false")
			}
			options.applied, options.appliedSet = raw == "true", true
			return nil
		})
	}
	if withLimit {
		flags.Func("limit", "migration count", func(value string) error {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid limit %q", value)
			}
			options.limit = parsed
			options.limitSet = true
			return nil
		})
	}
	if err := flags.Parse(args); err != nil {
		return cliOptions{}, nil, err
	}
	if options.timeout <= 0 || options.lockTimeout <= 0 || options.releaseTimeout <= 0 {
		return cliOptions{}, nil, errors.New("migration: timeouts must be positive")
	}
	return options, flags.Args(), nil
}

func (cli *CLI) runner(ctx context.Context, options cliOptions) (*Runner, func(), error) {
	if cli.Open == nil {
		return nil, nil, errors.New("migration: CLI Open function must not be nil")
	}
	connection, err := cli.Open(ctx, frameworkdb.Config{
		Driver:  options.driver,
		DSN:     options.dsn,
		Dialect: options.dialect,
	})
	if err != nil {
		return nil, nil, err
	}
	closeConnection := func() { _ = connection.Close() }
	runner, err := NewRunner(connection, DirectorySource{Path: options.directory}, Options{Table: options.table, Now: cli.Now, LockTimeout: options.lockTimeout, ReleaseTimeout: options.releaseTimeout})
	if err != nil {
		closeConnection()
		return nil, nil, err
	}
	return runner, closeConnection, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
