// Command rest-api demonstrates a JSON-only Chitu microservice protected by
// short-lived HMAC-SHA256 JSON Web Tokens.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
	"github.com/chitushka/framework/web"
)

type claims struct {
	Subject string `json:"sub"`
	Expires int64  `json:"exp"`
}

type tokenService struct {
	secret []byte
	now    func() time.Time
}

func (service tokenService) issue(subject string, ttl time.Duration) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims{Subject: subject, Expires: service.now().Add(ttl).Unix()})
	if err != nil {
		return "", err
	}
	unsigned := encode(header) + "." + encode(payload)
	signature := hmac.New(sha256.New, service.secret)
	_, _ = signature.Write([]byte(unsigned))
	return unsigned + "." + encode(signature.Sum(nil)), nil
}

func (service tokenService) verify(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, errors.New("token must have three parts")
	}
	headerPayload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims{}, errors.New("invalid token header")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(headerPayload, &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return claims{}, errors.New("unsupported token header")
	}
	actual, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims{}, errors.New("invalid token signature")
	}
	expected := hmac.New(sha256.New, service.secret)
	_, _ = expected.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(actual, expected.Sum(nil)) {
		return claims{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errors.New("invalid token payload")
	}
	var result claims
	if err := json.Unmarshal(payload, &result); err != nil || result.Subject == "" || result.Expires == 0 {
		return claims{}, errors.New("invalid token claims")
	}
	if service.now().Unix() >= result.Expires {
		return claims{}, errors.New("token expired")
	}
	return result, nil
}

func encode(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

type subjectKey struct{}

func bearer(service tokenService) web.Middleware {
	return func(next web.Handler) web.Handler {
		return func(ctx *web.Context) error {
			value := ctx.Request.Header.Get("Authorization")
			if !strings.HasPrefix(value, "Bearer ") {
				return web.NewHTTPError(http.StatusUnauthorized, "bearer token required", nil)
			}
			identity, err := service.verify(strings.TrimPrefix(value, "Bearer "))
			if err != nil {
				return web.NewHTTPError(http.StatusUnauthorized, "invalid bearer token", err)
			}
			ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), subjectKey{}, identity.Subject))
			return next(ctx)
		}
	}
}

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

func newHandler(secret []byte, now func() time.Time) (http.Handler, error) {
	if len(secret) < 32 {
		return nil, errors.New("rest-api: JWT secret must contain at least 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	tokens := tokenService{secret: append([]byte(nil), secret...), now: now}
	store := newArticles()
	router := web.NewRouter()
	if err := router.Use(web.Recover); err != nil {
		return nil, err
	}
	if err := router.POST("/v1/token", func(ctx *web.Context) error {
		var input struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		}
		if err := ctx.BindJSON(&input); err != nil {
			return err
		}
		if input.ClientID != "example-client" || input.ClientSecret != "change-me" {
			return web.NewHTTPError(http.StatusUnauthorized, "invalid client credentials", nil)
		}
		token, err := tokens.issue(input.ClientID, 15*time.Minute)
		if err != nil {
			return err
		}
		return ctx.JSON(http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 900})
	}); err != nil {
		return nil, err
	}
	if err := router.Group("/v1", func(api *web.Group) error {
		if err := api.Use(bearer(tokens)); err != nil {
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
	secret := []byte(os.Getenv("JWT_SECRET"))
	if len(secret) == 0 {
		secret = []byte("development-only-secret-change-me")
	}
	handler, err := newHandler(secret, time.Now)
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
