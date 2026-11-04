package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/auth"
	"github.com/golang-jwt/jwt/v5"
)

func exampleTokens(t *testing.T) (*auth.JWTVerifier, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewJWTVerifier(auth.JWTConfig{Issuer: "https://issuer.example", Audience: "articles", Algorithms: []string{"EdDSA"}}, []auth.JWTKey{{ID: "test", Algorithm: "EdDSA", PublicKey: public}})
	if err != nil {
		t.Fatal(err)
	}
	return verifier, private
}

func exampleToken(t *testing.T, key ed25519.PrivateKey, expires time.Time) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{
		Issuer: "https://issuer.example", Audience: jwt.ClaimStrings{"articles"}, Subject: "client",
		IssuedAt: jwt.NewNumericDate(expires.Add(-time.Minute)), ExpiresAt: jwt.NewNumericDate(expires),
	})
	token.Header["kid"], token.Header["typ"] = "test", "at+jwt"
	encoded, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestRESTAPIRequiresJWTAndCreatesArticle(t *testing.T) {
	verifier, key := exampleTokens(t)
	handler, err := newHandler(verifier)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/v1/articles", nil))
	if unauthorized.Code != 401 || unauthorized.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing bearer challenge")
	}
	created := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/articles", strings.NewReader(`{"title":"JWT example"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+exampleToken(t, key, time.Now().Add(time.Minute)))
	handler.ServeHTTP(created, request)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != "/v1/articles/2" {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	removed := httptest.NewRecorder()
	handler.ServeHTTP(removed, httptest.NewRequest("POST", "/v1/token", nil))
	if removed.Code != 404 {
		t.Fatal("built-in token issuer remains enabled")
	}
}

func TestRESTAPIRejectsTamperedExpiredAndRevokedTokens(t *testing.T) {
	verifier, key := exampleTokens(t)
	handler, err := newHandler(verifier)
	if err != nil {
		t.Fatal(err)
	}
	valid := exampleToken(t, key, time.Now().Add(time.Minute))
	for _, token := range []string{valid + "x", exampleToken(t, key, time.Now().Add(-time.Second))} {
		request := httptest.NewRequest("GET", "/v1/articles", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 401 {
			t.Fatal("invalid token accepted")
		}
	}
	if err := verifier.ReplaceKeys(nil); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/v1/articles", nil)
	request.Header.Set("Authorization", "Bearer "+valid)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("revoked key accepted")
	}
	if _, err := newHandler(nil); err == nil {
		t.Fatal("unconfigured verifier accepted")
	}
}
