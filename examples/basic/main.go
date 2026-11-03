// Command basic runs the minimal Chitu web application: session login and an
// AdminKit-powered dashboard. Demo credentials are admin / changeme.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

type demoIdentity string

func (identity demoIdentity) ID() string { return string(identity) }

type demoUsers struct{}

func (demoUsers) FindIdentity(_ context.Context, id string) (auth.Identity, error) {
	if id != "1" {
		return nil, nil
	}
	return demoIdentity(id), nil
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// newHandler builds the application separately from main so the complete
// login flow can be exercised with httptest.
func newHandler(publicDir string) (http.Handler, error) {
	sessions, err := session.New(session.NewMemoryBackend(), session.Config{
		Name: "basic_session", TTL: 8 * time.Hour,
	})
	if err != nil {
		return nil, err
	}

	router := web.NewRouter()
	if err := router.Use(web.Recover, sessions.Middleware, auth.Middleware(demoUsers{})); err != nil {
		return nil, err
	}
	if err := router.GET("/", func(ctx *web.Context) error {
		if auth.Must(ctx.Request.Context()).IsGuest() {
			return ctx.Redirect(http.StatusSeeOther, "/login")
		}
		return ctx.Redirect(http.StatusSeeOther, "/dashboard")
	}); err != nil {
		return nil, err
	}
	if err := router.GET("/login", func(ctx *web.Context) error {
		return ctx.HTML(http.StatusOK, loginPage)
	}, web.WithMiddleware(auth.RequireGuest)); err != nil {
		return nil, err
	}
	if err := router.POST("/login", func(ctx *web.Context) error {
		var input credentials
		if err := ctx.BindJSON(&input); err != nil {
			return err
		}
		if input.Username != "admin" || input.Password != "changeme" {
			return web.NewHTTPError(http.StatusUnauthorized, "invalid credentials", nil)
		}
		if err := auth.Must(ctx.Request.Context()).Login(demoIdentity("1")); err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, map[string]string{"redirect": "/dashboard"})
	}, web.WithMiddleware(auth.RequireGuest)); err != nil {
		return nil, err
	}
	if err := router.GET("/dashboard", func(ctx *web.Context) error {
		return ctx.HTML(http.StatusOK, dashboardPage)
	}, web.WithMiddleware(auth.RequireAuthenticated)); err != nil {
		return nil, err
	}
	if err := router.POST("/logout", func(ctx *web.Context) error {
		if err := auth.Must(ctx.Request.Context()).Logout(); err != nil {
			return err
		}
		return ctx.NoContent(http.StatusNoContent)
	}, web.WithMiddleware(auth.RequireAuthenticated)); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/", router)
	if publicDir != "" {
		info, statErr := os.Stat(publicDir)
		if statErr != nil || !info.IsDir() {
			return nil, fmt.Errorf("basic: public directory %q: %w", publicDir, statErr)
		}
		mux.Handle("/assets/", http.FileServer(http.Dir(publicDir)))
	}
	return mux, nil
}

func main() {
	publicDir := filepath.Clean("public")
	handler, err := newHandler(publicDir)
	if err != nil {
		log.Fatal(err)
	}
	server, err := web.NewServer(web.ServerConfig{Address: ":8080", ShutdownTimeout: 5 * time.Second}, handler)
	if err != nil {
		log.Fatal(err)
	}
	application := app.New(nil, nil)
	if err := application.AddModule(server); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := application.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

const loginPage = `<!doctype html><html><head><meta charset="utf-8"><title>Chitu Basic — Login</title><link rel="stylesheet" href="/assets/main-BJEwN-Dz.css"></head><body><main class="container py-5"><h1>Chitu Basic</h1><p>POST JSON credentials to <code>/login</code>: admin / changeme.</p></main></body></html>`

const dashboardPage = `<!doctype html><html><head><meta charset="utf-8"><title>Chitu Basic — Dashboard</title><link rel="stylesheet" href="/assets/main-BJEwN-Dz.css"></head><body><main class="container py-5"><h1>AdminKit dashboard</h1><div class="card"><div class="card-body">You are authenticated with a Chitu server-side session.</div></div></main></body></html>`
