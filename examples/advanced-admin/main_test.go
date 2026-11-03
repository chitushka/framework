package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func loginAs(t *testing.T, handler http.Handler, username string) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"`+username+`"}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) == 0 {
		t.Fatalf("login %s: status=%d body=%s", username, response.Code, response.Body.String())
	}
	return response.Result().Cookies()[0]
}

func requestWithCookie(handler http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.AddCookie(cookie)
	handler.ServeHTTP(response, request)
	return response
}

func TestAdvancedAdminRBACAndCRUD(t *testing.T) {
	handler, err := newHandler()
	if err != nil {
		t.Fatal(err)
	}
	editor := loginAs(t, handler, "editor")
	if response := requestWithCookie(handler, http.MethodGet, "/admin/dictionaries", "", editor); response.Code != http.StatusOK {
		t.Fatalf("editor list status = %d", response.Code)
	}
	if response := requestWithCookie(handler, http.MethodPost, "/admin/dictionaries", `{"code":"new","name":"New"}`, editor); response.Code != http.StatusForbidden {
		t.Fatalf("editor create status = %d", response.Code)
	}

	admin := loginAs(t, handler, "admin")
	created := requestWithCookie(handler, http.MethodPost, "/admin/dictionaries", `{"code":"new","name":"New"}`, admin)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"id":2`) {
		t.Fatalf("admin create status=%d body=%s", created.Code, created.Body.String())
	}
	if response := requestWithCookie(handler, http.MethodDelete, "/admin/dictionaries/2", "", admin); response.Code != http.StatusNoContent {
		t.Fatalf("admin delete status = %d", response.Code)
	}
	if response := requestWithCookie(handler, http.MethodGet, "/admin/rbac", "", admin); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "dictionary.manage") {
		t.Fatalf("rbac status=%d body=%s", response.Code, response.Body.String())
	}
}
