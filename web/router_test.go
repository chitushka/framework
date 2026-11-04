package web_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/chitushka/framework/web"
)

func TestRouterMatchesParamsAndProducesJSON(t *testing.T) {
	router := web.NewRouter()
	err := router.GET("/users/{id}", func(context *web.Context) error {
		return context.JSON(http.StatusOK, map[string]string{"id": context.Param("id")})
	}, web.WithName("users.view"))
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/users/42", nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("неверный ответ: %d %s", response.Code, response.Header())
	}
	if strings.TrimSpace(response.Body.String()) != `{"id":"42"}` {
		t.Fatalf("неверное тело: %s", response.Body.String())
	}

	generated, err := router.URL("users.view", map[string]string{"id": "Иван Петров"})
	if err != nil {
		t.Fatal(err)
	}
	if generated != "/users/%D0%98%D0%B2%D0%B0%D0%BD%20%D0%9F%D0%B5%D1%82%D1%80%D0%BE%D0%B2" {
		t.Fatalf("неверный URL: %s", generated)
	}
}

func TestRouterReturnsNotFoundAndMethodNotAllowed(t *testing.T) {
	router := web.NewRouter()
	_ = router.GET("/known", func(*web.Context) error { return nil })

	for _, test := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/missing", http.StatusNotFound},
		{http.MethodPost, "/known", http.StatusMethodNotAllowed},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.status {
			t.Fatalf("%s %s: получен %d", test.method, test.path, response.Code)
		}
	}
}

func TestMiddlewareOrderAndErrorHandling(t *testing.T) {
	router := web.NewRouter()
	var calls []string
	middleware := func(name string) web.Middleware {
		return func(next web.Handler) web.Handler {
			return func(context *web.Context) error {
				calls = append(calls, name+":before")
				err := next(context)
				calls = append(calls, name+":after")
				return err
			}
		}
	}
	if err := router.Use(web.Recover, middleware("one"), middleware("two")); err != nil {
		t.Fatal(err)
	}
	_ = router.GET("/failure", func(*web.Context) error {
		calls = append(calls, "handler")
		return errors.New("boom")
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/failure", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("получен статус %d", response.Code)
	}
	want := []string{"one:before", "two:before", "handler", "two:after", "one:after"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("порядок middleware: %v", calls)
	}
}

func TestRecoverConvertsPanicToError(t *testing.T) {
	router := web.NewRouter()
	if err := router.Use(web.Recover); err != nil {
		t.Fatal(err)
	}
	_ = router.GET("/panic", func(*web.Context) error { panic("boom") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("получен статус %d", response.Code)
	}
}

func TestContextBindJSON(t *testing.T) {
	type input struct {
		Name string `json:"name"`
	}
	router := web.NewRouter()
	_ = router.POST("/input", func(context *web.Context) error {
		var value input
		if err := context.BindJSON(&value); err != nil {
			return err
		}
		return context.NoContent(http.StatusCreated)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/input", bytes.NewBufferString(`{"name":"Chitu"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("получен статус %d", response.Code)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/input", bytes.NewBufferString(`{"unknown":1}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("получен статус %d", response.Code)
	}
}

func TestRouterValidationAndCustomErrorHandler(t *testing.T) {
	router := web.NewRouter()
	handler := func(*web.Context) error { return errors.New("failure") }
	for _, test := range []struct {
		name string
		err  error
	}{
		{"invalid pattern", router.GET("users", handler)},
		{"invalid param", router.GET("/users/{id", handler)},
		{"nil handler", router.GET("/users", nil)},
		{"nil middleware", router.Use(nil)},
		{"nil error handler", router.SetErrorHandler(nil)},
	} {
		if test.err == nil {
			t.Fatalf("%s: ожидалась ошибка", test.name)
		}
	}

	want := errors.New("failure")
	var received error
	_ = router.SetErrorHandler(func(context *web.Context, err error) {
		received = err
		_ = context.NoContent(599)
	})
	_ = router.GET("/failure", func(*web.Context) error { return want })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/failure", nil))
	if !errors.Is(received, want) || response.Code != 599 {
		t.Fatalf("custom error handler: %v, %d", received, response.Code)
	}
}

func TestRouteGroupsApplyPrefixAndMiddleware(t *testing.T) {
	router := web.NewRouter()
	var calls []string
	mark := func(name string) web.Middleware {
		return func(next web.Handler) web.Handler {
			return func(context *web.Context) error {
				calls = append(calls, name)
				return next(context)
			}
		}
	}
	if err := router.Use(mark("global")); err != nil {
		t.Fatal(err)
	}
	err := router.Group("/api", func(api *web.Group) error {
		if err := api.Use(mark("api")); err != nil {
			return err
		}
		return api.Group("/v1", func(v1 *web.Group) error {
			if err := v1.Use(mark("v1")); err != nil {
				return err
			}
			return v1.GET("/users", func(context *web.Context) error {
				calls = append(calls, "handler")
				return context.NoContent(http.StatusNoContent)
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("получен статус %d", response.Code)
	}
	want := []string{"global", "api", "v1", "handler"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("неверный порядок: %v", calls)
	}
}

func TestRouterMatchesConstraintsAndDomainParameters(t *testing.T) {
	router := web.NewRouter()
	err := router.GET("/users/{id}", func(context *web.Context) error {
		return context.JSON(http.StatusOK, map[string]string{
			"tenant": context.Param("tenant"),
			"id":     context.Param("id"),
		})
	},
		web.WithHost("{tenant}.example.test"),
		web.Where("tenant", `[a-z][a-z0-9-]*`),
		web.Where("id", `[0-9]+`),
		web.WithName("tenant.users.view"),
	)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://acme.example.test/users/42", nil)
	request.Host = "acme.example.test:8080"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tenant":"acme"`) {
		t.Fatalf("неверный ответ: %d %s", response.Code, response.Body.String())
	}

	for _, target := range []string{
		"http://acme.example.test/users/not-a-number",
		"http://other.test/users/42",
	} {
		response = httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s: получен статус %d", target, response.Code)
		}
	}

	generated, err := router.URL("tenant.users.view", map[string]string{"tenant": "acme", "id": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if generated != "//acme.example.test/users/42" {
		t.Fatalf("неверный URL: %s", generated)
	}
	if _, err := router.URL("tenant.users.view", map[string]string{"tenant": "bad.name", "id": "42"}); err == nil {
		t.Fatal("ожидалась ошибка host-параметра")
	}
	if _, err := router.URL("tenant.users.view", map[string]string{"tenant": "acme", "id": "abc"}); err == nil {
		t.Fatal("ожидалась ошибка ограничения")
	}
}

func TestGroupCanApplyHostToRoutes(t *testing.T) {
	router := web.NewRouter()
	err := router.Group("/admin", func(group *web.Group) error {
		if err := group.Host("admin.example.test"); err != nil {
			return err
		}
		return group.GET("/dashboard", func(context *web.Context) error {
			return context.NoContent(http.StatusNoContent)
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://admin.example.test/admin/dashboard", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("получен статус %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "http://public.example.test/admin/dashboard", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("получен статус %d", response.Code)
	}
}

func TestAdvancedRouteValidation(t *testing.T) {
	router := web.NewRouter()
	handler := func(*web.Context) error { return nil }
	for name, err := range map[string]error{
		"invalid regexp":   router.GET("/items/{id}", handler, web.Where("id", "[")),
		"unknown param":    router.GET("/items/{id}", handler, web.Where("other", ".+")),
		"duplicate param":  router.GET("/items/{id}", handler, web.WithHost("{id}.example.test")),
		"invalid host":     router.GET("/host", handler, web.WithHost("bad host")),
		"invalid prefix":   router.Group("api", func(*web.Group) error { return nil }),
		"nil configure":    router.Group("/api", nil),
		"nil route option": router.GET("/nil-option", handler, nil),
	} {
		if err == nil {
			t.Fatalf("%s: ожидалась ошибка", name)
		}
	}
}
