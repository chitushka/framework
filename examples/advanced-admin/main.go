// Command advanced-admin runs the persistent administrative starter.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/logging"
	"github.com/chitushka/framework/web"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	migrate := flag.Bool("migrate", false, "apply schema migrations, then exit")
	seed := flag.Bool("seed", false, "seed once using explicit ADMIN_SEED_*_HASH_FILE secrets, then exit")
	hash := flag.Bool("hash-password", false, "read a password from stdin and print its Argon2id PHC")
	flag.Parse()
	if *hash {
		if err := hashPassword(os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	c, err := loadConfig(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	handler, err := logging.NewProductionHandler(os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "admin: logger unavailable")
		os.Exit(1)
	}
	logger := slog.New(handler)
	if !c.production() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	if err := run(ctx, c, *migrate, *seed, logger); err != nil {
		logger.Error("Advanced Admin failed", "error", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, c configuration, migrate, seed bool, logger *slog.Logger) error {
	connection, err := db.Open(ctx, c.Database, db.NewDefaultDialectRegistry())
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	s, err := newStore(connection)
	if err != nil {
		return err
	}
	setup, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if migrate {
		if err = s.migrate(setup); err != nil {
			return err
		}
		logger.Info("Admin migrations applied")
	}
	if seed {
		a, err := readHashFile(c.AdminHashFile)
		if err != nil {
			return err
		}
		e, err := readHashFile(c.EditorHashFile)
		if err != nil {
			return err
		}
		if err = s.seed(setup, a, e); err != nil {
			return err
		}
		logger.Info("Admin seed completed")
	}
	if migrate || seed {
		return nil
	}
	if err = s.schemaReady(setup); err != nil {
		return err
	}
	if err = s.checkSeeded(setup); err != nil {
		return err
	}
	site, err := newSite(s, c)
	if err != nil {
		return err
	}
	defer func() { _ = site.Close() }()
	application := app.New(nil, nil)
	if err = application.SetShutdownTimeout(12 * time.Second); err != nil {
		return err
	}
	server, err := web.NewServer(web.ServerConfig{Address: c.Address, ShutdownTimeout: 10 * time.Second}, site)
	if err != nil {
		return err
	}
	if err = application.AddModule(server); err != nil {
		return err
	}
	logger.Info("Starting Advanced Admin", "address", c.Address, "environment", c.Environment)
	return application.Run(ctx)
}
func hashPassword(input io.Reader, output io.Writer) error {
	value, err := io.ReadAll(io.LimitReader(input, 1027))
	if err != nil {
		return err
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r")
	clear(value)
	if len(password) < 12 || len(password) > 1024 {
		return invalid("Password must contain 12–1024 bytes")
	}
	h, err := auth.NewPasswordHasher(auth.PasswordConfig{})
	if err != nil {
		return err
	}
	encoded, err := h.Hash(context.Background(), password)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, encoded)
	return err
}
