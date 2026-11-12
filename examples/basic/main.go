// Command basic runs a single-process browser starter with session authentication.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/logging"
	"github.com/chitushka/framework/web"
)

func main() {
	hash := flag.Bool("hash-password", false, "read a password from stdin and print an Argon2id hash")
	flag.Parse()
	if *hash {
		if err := hashPassword(os.Stdin, os.Stdout); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	config, err := loadConfig(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logHandler, err := logging.NewProductionHandler(os.Stderr)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := slog.New(logHandler)
	if !config.production() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	if err := run(ctx, config, logger); err != nil {
		logger.Error("Basic stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, config configuration, logger *slog.Logger) error {
	site, err := newSite(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = site.Close() }()
	server, err := web.NewServer(web.ServerConfig{Address: config.Address, ShutdownTimeout: 8 * time.Second}, site)
	if err != nil {
		return err
	}
	application := app.New(nil, nil)
	if err := application.SetShutdownTimeout(10 * time.Second); err != nil {
		return err
	}
	if err := application.AddModule(server); err != nil {
		return err
	}
	logger.Info("Starting Chitu Basic", "address", config.Address, "environment", config.Environment)
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
		return errors.New("basic: password must contain 12–1024 bytes")
	}
	hasher, err := auth.NewPasswordHasher(auth.PasswordConfig{})
	if err != nil {
		return err
	}
	encoded, err := hasher.Hash(context.Background(), password)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, encoded)
	return err
}
