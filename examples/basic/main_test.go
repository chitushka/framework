package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBasicLoginAndDashboard(t *testing.T) {
	handler, err := newHandler("")
	if err != nil {
		t.Fatal(err)
	}
	guest := httptest.NewRecorder()
	handler.ServeHTTP(guest, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if guest.Code != http.StatusUnauthorized {
		t.Fatalf("guest status = %d", guest.Code)
	}

	login := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"admin","password":"changeme"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, request)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}

	dashboard := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(login.Result().Cookies()[0])
	handler.ServeHTTP(dashboard, request)
	if dashboard.Code != http.StatusOK || !strings.Contains(dashboard.Body.String(), "AdminKit dashboard") {
		t.Fatalf("dashboard status = %d body=%s", dashboard.Code, dashboard.Body.String())
	}
}
