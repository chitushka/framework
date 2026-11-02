package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

type identity string

func (value identity) ID() string { return string(value) }

type loader struct{}

func (loader) FindIdentity(_ context.Context, id string) (auth.Identity, error) {
	if id == "42" {
		return identity(id), nil
	}
	return nil, nil
}

func TestUserLoginIsRestoredFromSession(t *testing.T) {
	manager, _ := session.New(session.NewMemoryBackend(), session.Config{Name: "sid"})
	router := web.NewRouter()
	_ = router.Use(manager.Middleware, auth.Middleware(loader{}))
	_ = router.POST("/login", func(ctx *web.Context) error { return auth.Must(ctx.Request.Context()).Login(identity("42")) })
	_ = router.GET("/me", func(ctx *web.Context) error {
		return ctx.JSON(200, map[string]string{"id": auth.Must(ctx.Request.Context()).ID()})
	}, web.WithMiddleware(auth.RequireAuthenticated))
	login := httptest.NewRecorder()
	router.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/login", nil))
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %#v", cookies)
	}
	request := httptest.NewRequest(http.MethodGet, "/me", nil)
	request.AddCookie(cookies[0])
	me := httptest.NewRecorder()
	router.ServeHTTP(me, request)
	if me.Code != 200 || me.Body.String() != "{\"id\":\"42\"}\n" {
		t.Fatalf("me = %d %q", me.Code, me.Body.String())
	}
}

func TestRequireAuthenticatedRejectsGuest(t *testing.T) {
	manager, _ := session.New(session.NewMemoryBackend(), session.Config{})
	router := web.NewRouter()
	_ = router.Use(manager.Middleware, auth.Middleware(loader{}))
	_ = router.GET("/", func(*web.Context) error { return nil }, web.WithMiddleware(auth.RequireAuthenticated))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}
