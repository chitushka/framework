package security_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitushka/framework/security"
	"github.com/chitushka/framework/session"
	"github.com/chitushka/framework/web"
)

func TestCSRFFailsClosedOnSessionAndFormErrors(t *testing.T) {
	manager, err := session.New(session.NewMemoryBackend(), session.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"invalid session value", "destroyed session", "malformed form"} {
		t.Run(name, func(t *testing.T) {
			method, body := http.MethodGet, ""
			if name == "malformed form" {
				method, body = http.MethodPost, "_csrf=%zz"
			}
			request := httptest.NewRequest(method, "/", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			called := false
			next := security.CSRF(security.CSRFConfig{})(func(*web.Context) error { called = true; return nil })
			err := manager.Middleware(func(ctx *web.Context) error {
				current := session.Must(ctx.Request.Context())
				switch name {
				case "invalid session value":
					if err := current.Set("security.csrf", 123); err != nil {
						return err
					}
				case "destroyed session":
					current.Destroy()
				case "malformed form":
					if err := current.Set("security.csrf", "existing-token"); err != nil {
						return err
					}
				}
				return next(ctx)
			})(&web.Context{Writer: response, Request: request})
			if err == nil || called || response.Body.Len() != 0 {
				t.Fatalf("CSRF error exposed protected handler: %v", err)
			}
			if name == "malformed form" {
				var httpError *web.HTTPError
				if !errors.As(err, &httpError) || httpError.Status != http.StatusBadRequest {
					t.Fatal("malformed form did not report 400", err)
				}
			}
		})
	}
}
