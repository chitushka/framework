package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chitushka/framework/web"
)

type verifierFixture struct {
	principal Principal
	err       error
}

func (v verifierFixture) Verify(context.Context, string) (Principal, error) {
	return v.principal, v.err
}

func TestBearerTransportAndFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verifier TokenVerifier
		headers  []string
		query    string
		status   int
	}{
		{"valid", verifierFixture{principal: Principal{Subject: "alice"}}, []string{"bearer token"}, "", 204},
		{"missing", verifierFixture{}, nil, "", 401},
		{"duplicate", verifierFixture{}, []string{"Bearer a", "Bearer b"}, "", 401},
		{"query", verifierFixture{}, nil, "?access_token=token", 401},
		{"wrong scheme", verifierFixture{}, []string{"Basic token"}, "", 401},
		{"oversized", verifierFixture{}, []string{"Bearer " + strings.Repeat("x", 8193)}, "", 401},
		{"invalid", verifierFixture{err: ErrInvalidToken}, []string{"Bearer token"}, "", 401},
		{"backend", verifierFixture{err: errors.New("private key path /secret")}, []string{"Bearer token"}, "", 503},
		{"nil", nil, []string{"Bearer token"}, "", 503},
		{"empty principal", verifierFixture{}, []string{"Bearer token"}, "", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := web.NewRouter()
			if err := router.Use(Bearer(tc.verifier)); err != nil {
				t.Fatal(err)
			}
			if err := router.GET("/", func(ctx *web.Context) error {
				principal, ok := PrincipalFromContext(ctx.Request.Context())
				if !ok || principal.Subject != "alice" {
					t.Fatal("principal not propagated")
				}
				return ctx.NoContent(204)
			}); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", "/"+tc.query, nil)
			for _, header := range tc.headers {
				request.Header.Add("Authorization", header)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("unsafe response: %d %s", response.Code, response.Body.String())
			}
			if tc.status == 401 && response.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing bearer challenge")
			}
		})
	}
}
