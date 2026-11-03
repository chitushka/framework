package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRESTAPIRequiresJWTAndCreatesArticle(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	handler, err := newHandler([]byte("12345678901234567890123456789012"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/articles", nil))
	if unauthorized.Code != http.StatusUnauthorized || !strings.Contains(unauthorized.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unauthorized status=%d type=%s", unauthorized.Code, unauthorized.Header().Get("Content-Type"))
	}

	tokenResponse := httptest.NewRecorder()
	handler.ServeHTTP(tokenResponse, httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"client_id":"example-client","client_secret":"change-me"}`)))
	if tokenResponse.Code != http.StatusOK {
		t.Fatalf("token status=%d body=%s", tokenResponse.Code, tokenResponse.Body.String())
	}
	var tokenBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tokenResponse.Body.Bytes(), &tokenBody); err != nil {
		t.Fatal(err)
	}

	created := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/articles", strings.NewReader(`{"title":"JWT example"}`))
	request.Header.Set("Authorization", "Bearer "+tokenBody.AccessToken)
	handler.ServeHTTP(created, request)
	if created.Code != http.StatusCreated || created.Header().Get("Location") != "/v1/articles/2" {
		t.Fatalf("create status=%d location=%s body=%s", created.Code, created.Header().Get("Location"), created.Body.String())
	}
}

func TestRESTAPIRejectsTamperedAndExpiredTokens(t *testing.T) {
	now := time.Unix(1000, 0)
	service := tokenService{secret: []byte("12345678901234567890123456789012"), now: func() time.Time { return now }}
	token, err := service.issue("client", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.verify(token + "x"); err == nil {
		t.Fatal("tampered token accepted")
	}
	service.now = func() time.Time { return now.Add(time.Second) }
	if _, err := service.verify(token); err == nil {
		t.Fatal("expired token accepted")
	}
}
