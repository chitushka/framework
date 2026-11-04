// Command rest-api demonstrates a JSON-only Chitu microservice protected by
// externally issued Ed25519 access tokens verified by the reusable auth package.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chitushka/framework/app"
	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/web"
	"github.com/golang-jwt/jwt/v5"
)

type article struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

type articles struct {
	mu     sync.RWMutex
	nextID int
	values map[int]article
}

func newArticles() *articles {
	return &articles{nextID: 2, values: map[int]article{1: {ID: 1, Title: "Welcome to Chitu"}}}
}

func (store *articles) list() []article {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]article, 0, len(store.values))
	for id := 1; id < store.nextID; id++ {
		if item, found := store.values[id]; found {
			result = append(result, item)
		}
	}
	return result
}

func (store *articles) create(item article) article {
	store.mu.Lock()
	item.ID = store.nextID
	store.nextID++
	store.values[item.ID] = item
	store.mu.Unlock()
	return item
}

func newHandler(verifier auth.TokenVerifier) (http.Handler, error) {
	if verifier == nil {
		return nil, errors.New("rest-api: token verifier is required")
	}
	store := newArticles()
	router := web.NewRouter()
	if err := router.Use(web.Recover); err != nil {
		return nil, err
	}
	if err := router.Group("/v1", func(api *web.Group) error {
		if err := api.Use(auth.Bearer(verifier)); err != nil {
			return err
		}
		if err := api.GET("/articles", func(ctx *web.Context) error {
			return ctx.JSON(http.StatusOK, map[string]any{"data": store.list()})
		}); err != nil {
			return err
		}
		if err := api.GET("/articles/{id}", func(ctx *web.Context) error {
			id, _ := strconv.Atoi(ctx.Param("id"))
			store.mu.RLock()
			item, found := store.values[id]
			store.mu.RUnlock()
			if !found {
				return web.NewHTTPError(http.StatusNotFound, "article not found", nil)
			}
			return ctx.JSON(http.StatusOK, map[string]any{"data": item})
		}, web.Where("id", `[0-9]+`)); err != nil {
			return err
		}
		return api.POST("/articles", func(ctx *web.Context) error {
			var item article
			if err := ctx.BindJSON(&item); err != nil {
				return err
			}
			if strings.TrimSpace(item.Title) == "" {
				return web.NewHTTPError(http.StatusUnprocessableEntity, "title is required", nil)
			}
			created := store.create(item)
			ctx.Writer.Header().Set("Location", fmt.Sprintf("/v1/articles/%d", created.ID))
			return ctx.JSON(http.StatusCreated, map[string]any{"data": created})
		})
	}); err != nil {
		return nil, err
	}
	return router, nil
}

func main() {
	keyPEM, err := os.ReadFile(os.Getenv("JWT_PUBLIC_KEY_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	key, err := jwt.ParseEdPublicKeyFromPEM(keyPEM)
	if err != nil {
		log.Fatal(err)
	}
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{
		Issuer: os.Getenv("JWT_ISSUER"), Audience: os.Getenv("JWT_AUDIENCE"), Algorithms: []string{"EdDSA"},
	}, []auth.JWTKey{{ID: os.Getenv("JWT_KEY_ID"), Algorithm: "EdDSA", PublicKey: key}})
	if err != nil {
		log.Fatal(err)
	}
	handler, err := newHandler(verifier)
	if err != nil {
		log.Fatal(err)
	}
	server, err := web.NewServer(web.ServerConfig{Address: ":8082", ShutdownTimeout: 5 * time.Second}, handler)
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
