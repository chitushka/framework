package session_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

func TestManagerPersistsAndRegeneratesSession(t *testing.T) {
	manager, err := session.New(session.NewMemoryBackend(), session.Config{Name: "sid"})
	if err != nil {
		t.Fatal(err)
	}
	router := web.NewRouter()
	if err := router.Use(manager.Middleware); err != nil {
		t.Fatal(err)
	}
	if err := router.POST("/set", func(ctx *web.Context) error { return session.Must(ctx.Request.Context()).Set("name", "Ada") }); err != nil {
		t.Fatal(err)
	}
	if err := router.GET("/get", func(ctx *web.Context) error {
		var name string
		ok, err := session.Must(ctx.Request.Context()).Get("name", &name)
		if err != nil {
			return err
		}
		if !ok {
			return ctx.NoContent(404)
		}
		return ctx.JSON(200, name)
	}); err != nil {
		t.Fatal(err)
	}

	set := httptest.NewRecorder()
	router.ServeHTTP(set, httptest.NewRequest(http.MethodPost, "/set", nil))
	if set.Code != 200 {
		t.Fatalf("set status = %d, body=%s", set.Code, set.Body.String())
	}
	cookies := set.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %#v", cookies)
	}
	getRequest := httptest.NewRequest(http.MethodGet, "/get", nil)
	getRequest.AddCookie(cookies[0])
	get := httptest.NewRecorder()
	router.ServeHTTP(get, getRequest)
	if get.Code != 200 || get.Body.String() != "\"Ada\"\n" {
		t.Fatalf("get = %d %q", get.Code, get.Body.String())
	}
}

func TestSessionRejectsInvalidCookieAndDestroyedState(t *testing.T) {
	manager, _ := session.New(session.NewMemoryBackend(), session.Config{Name: "sid"})
	router := web.NewRouter()
	_ = router.Use(manager.Middleware)
	_ = router.GET("/", func(ctx *web.Context) error {
		value := session.Must(ctx.Request.Context())
		value.Destroy()
		return value.Set("x", 1)
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: "sid", Value: "attacker-controlled"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 500 {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestCookieStoreEncryptsAndRestoresValues(t *testing.T) {
	store, err := session.NewCookieStore([]byte("0123456789abcdef0123456789abcdef"), session.Config{Name: "state"})
	if err != nil {
		t.Fatal(err)
	}
	router := web.NewRouter()
	_ = router.Use(store.Middleware)
	_ = router.POST("/set", func(ctx *web.Context) error { return session.Must(ctx.Request.Context()).Set("role", "admin") })
	_ = router.GET("/get", func(ctx *web.Context) error {
		var role string
		found, err := session.Must(ctx.Request.Context()).Get("role", &role)
		if err != nil {
			return err
		}
		if !found {
			return ctx.NoContent(404)
		}
		return ctx.JSON(200, role)
	})
	set := httptest.NewRecorder()
	router.ServeHTTP(set, httptest.NewRequest(http.MethodPost, "/set", nil))
	cookies := set.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == "" || cookies[0].Value == "admin" {
		t.Fatalf("cookie = %#v", cookies)
	}
	request := httptest.NewRequest(http.MethodGet, "/get", nil)
	request.AddCookie(cookies[0])
	get := httptest.NewRecorder()
	router.ServeHTTP(get, request)
	if get.Code != 200 || get.Body.String() != "\"admin\"\n" {
		t.Fatalf("get = %d %q", get.Code, get.Body.String())
	}
	cookies[0].Value = cookies[0].Value[:len(cookies[0].Value)-1] + "A"
	tamperedRequest := httptest.NewRequest(http.MethodGet, "/get", nil)
	tamperedRequest.AddCookie(cookies[0])
	tampered := httptest.NewRecorder()
	router.ServeHTTP(tampered, tamperedRequest)
	if tampered.Code != 404 {
		t.Fatalf("tampered status = %d", tampered.Code)
	}
}
