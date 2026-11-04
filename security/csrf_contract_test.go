package security_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/security"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

type csrfIdentity string

func (id csrfIdentity) ID() string { return string(id) }

type csrfLoader struct{}

func (csrfLoader) FindIdentity(context.Context, string) (auth.Identity, error) {
	return csrfIdentity("alice"), nil
}

func TestCSRFTransportsAndAuthenticationTransitions(t *testing.T) {
	manager, err := session.New(session.NewMemoryBackend(), session.Config{})
	if err != nil {
		t.Fatal(err)
	}
	router := web.NewRouter()
	if err := router.Use(manager.Middleware, security.CSRF(security.CSRFConfig{}), auth.Middleware(csrfLoader{})); err != nil {
		t.Fatal(err)
	}
	if err := router.GET("/token", func(ctx *web.Context) error { return ctx.NoContent(204) }); err != nil {
		t.Fatal(err)
	}
	if err := router.POST("/login", func(ctx *web.Context) error { return auth.Must(ctx.Request.Context()).Login(csrfIdentity("alice")) }); err != nil {
		t.Fatal(err)
	}
	if err := router.POST("/logout", func(ctx *web.Context) error { return auth.Must(ctx.Request.Context()).Logout() }); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, contentType, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		if token != "" {
			request.Header.Set("X-CSRF-Token", token)
		}
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	initial := call("GET", "/token", "", "", "", nil)
	cookie := initial.Result().Cookies()[0]
	token := initial.Header().Get("X-CSRF-Token")
	if token == "" || initial.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing token/cache policy")
	}
	duplicate := httptest.NewRequest("POST", "/login", nil)
	duplicate.AddCookie(cookie)
	duplicate.Header.Add("X-CSRF-Token", token)
	duplicate.Header.Add("X-CSRF-Token", token)
	duplicateResponse := httptest.NewRecorder()
	router.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != 403 {
		t.Fatal("duplicate CSRF headers accepted")
	}
	var multipartBody bytes.Buffer
	writer := multipart.NewWriter(&multipartBody)
	if err := writer.WriteField("_csrf", token); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, body, contentType, header string
		status                                int
	}{
		{"query rejected", "/login?_csrf=" + url.QueryEscape(token), "", "", "", 403},
		{"missing", "/login", "", "", "", 403},
		{"json missing header", "/login", `{"_csrf":"` + token + `"}`, "application/json", "", 403},
		{"wrong header", "/login", "", "application/json", "wrong", 403},
		{"duplicate form", "/login", "_csrf=" + token + "&_csrf=" + token, "application/x-www-form-urlencoded", "", 403},
		{"body form", "/login", "_csrf=" + token, "application/x-www-form-urlencoded", "", 200},
		{"multipart", "/login", multipartBody.String(), writer.FormDataContentType(), "", 200},
		{"json header", "/login", `{}`, "application/json", token, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Every successful login revokes the previous ID, so establish a fresh
			// session for each case while preserving the transport under test.
			fresh := call("GET", "/token", "", "", "", nil)
			freshToken := fresh.Header().Get("X-CSRF-Token")
			body := strings.ReplaceAll(tc.body, token, freshToken)
			path := strings.ReplaceAll(tc.path, token, freshToken)
			header := strings.ReplaceAll(tc.header, token, freshToken)
			got := call("POST", path, body, tc.contentType, header, fresh.Result().Cookies()[0])
			if got.Code != tc.status {
				t.Fatalf("status %d: %s", got.Code, got.Body.String())
			}
		})
	}
	other := call("GET", "/token", "", "", "", nil)
	if got := call("POST", "/login", "", "", other.Header().Get("X-CSRF-Token"), cookie); got.Code != 403 {
		t.Fatal("accepted another session's token")
	}
	login := call("POST", "/login", "{}", "application/json", token, cookie)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	loggedIn := login.Result().Cookies()[0]
	if loggedIn.Value == cookie.Value {
		t.Fatal("login did not rotate identifier")
	}
	if got := call("POST", "/logout", "", "", token, loggedIn); got.Code != 403 {
		t.Fatal("login retained anonymous CSRF token")
	}
	if got := call("POST", "/login", "", "", token, cookie); got.Code != 403 {
		t.Fatal("old ID replay accepted")
	}
	newToken := call("GET", "/token", "", "", "", loggedIn)
	logout := call("POST", "/logout", "", "", newToken.Header().Get("X-CSRF-Token"), loggedIn)
	if logout.Code != 200 || logout.Result().Cookies()[0].Value == loggedIn.Value {
		t.Fatal("logout failed to rotate identifier")
	}
	if got := call("POST", "/logout", "", "", newToken.Header().Get("X-CSRF-Token"), loggedIn); got.Code != 403 {
		t.Fatal("logged-out cookie replay accepted")
	}
}
