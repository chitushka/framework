// Command rest-api runs a bounded, single-process JSON API with external JWT authentication.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/web"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	config, verifier, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	handler, err := configuredHandler(verifier, config)
	if err != nil {
		return err
	}
	server, err := web.NewServer(web.ServerConfig{Address: config.Address, ShutdownTimeout: 5 * time.Second, RequestLimits: web.RequestLimits{MaxBodyBytes: 4096}}, handler,
		web.WithServerHandlerWrapper(errorContract))
	if err != nil {
		return err
	}
	application := app.New(nil, nil)
	if err := application.SetShutdownTimeout(7 * time.Second); err != nil {
		return err
	}
	if err := application.AddModule(server); err != nil {
		return err
	}
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
