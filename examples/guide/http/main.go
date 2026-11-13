// Command http is a minimal loopback service with bounded shutdown and health.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/web"
)

func service(address string) (*app.Application, *web.Server, error) {
	application := app.New(nil, nil)
	if err := application.SetShutdownTimeout(7 * time.Second); err != nil {
		return nil, nil, err
	}
	health, err := web.NewHealth(application, web.HealthConfig{ProbeTimeout: 200 * time.Millisecond})
	if err != nil {
		return nil, nil, err
	}
	router := web.NewRouter()
	if err := router.GET("/hello", func(ctx *web.Context) error {
		return ctx.JSON(http.StatusOK, map[string]string{"message": "Hello, Chitu"})
	}); err != nil {
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", health.Liveness)
	mux.HandleFunc("/readyz", health.Readiness)
	mux.Handle("/", router)
	handler, err := web.NewProductionHandler(mux)
	if err != nil {
		return nil, nil, err
	}
	server, err := web.NewServer(web.ServerConfig{Address: address, ShutdownTimeout: 5 * time.Second}, handler)
	if err != nil {
		return nil, nil, err
	}
	if err := application.AddModule(server); err != nil {
		return nil, nil, err
	}
	return application, server, nil
}

func main() {
	application, _, err := service("127.0.0.1:8080")
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := app.SignalContext(context.Background())
	defer stop()
	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
