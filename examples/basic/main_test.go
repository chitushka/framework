package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chitushka/framework/auth"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) configuration {
	t.Helper()
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ".vite"), 0700))
	must(t, os.MkdirAll(filepath.Join(root, "assets"), 0700))
	// Arbitrary hashes prove the application uses manifest data rather than names.
	manifest := map[string]any{"src/basic.js": map[string]any{"file": "assets/entry-test987.js", "css": []string{"assets/theme-test654.css"}}}
	data, err := json.Marshal(manifest)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(root, ".vite", "manifest.json"), data, 0600))
	must(t, os.WriteFile(filepath.Join(root, "assets", "entry-test987.js"), []byte(`document.documentElement.dataset.starter="basic"`), 0600))
	must(t, os.WriteFile(filepath.Join(root, "assets", "theme-test654.css"), []byte("body{color:blue}"), 0600))
	c, err := loadConfig(func(string) string { return "" })
	must(t, err)
	c.PublicDir = root
	return c
}

func testSite(t *testing.T, c configuration) *site {
	t.Helper()
	s, err := newSite(t.Context(), c)
	must(t, err)
	t.Cleanup(func() { must(t, s.Close()) })
	return s
}

func call(t *testing.T, h http.Handler, method, path, body, contentType, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		r.Header.Set("X-CSRF-Token", token)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func cookieOf(t *testing.T, r *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	response := r.Result()
	defer func() { must(t, response.Body.Close()) }()
	for _, c := range response.Cookies() {
		if strings.HasSuffix(c.Name, "basic_session") {
			return c
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func TestBrowserLoginDashboardLogoutAndRevocation(t *testing.T) {
	s := testSite(t, fixture(t))
	guest := call(t, s, "GET", "/dashboard", "", "", "", nil)
	if guest.Code != 303 || guest.Header().Get("Location") != "/login" {
		t.Fatal("guest dashboard")
	}
	login := call(t, s, "GET", "/login", "", "", "", nil)
	token := login.Header().Get("X-CSRF-Token")
	anonymous := cookieOf(t, login)
	if token == "" || !strings.Contains(login.Body.String(), `name="_csrf" value="`+token+`"`) || !anonymous.HttpOnly || anonymous.SameSite != http.SameSiteLaxMode {
		t.Fatal("form CSRF/cookie policy")
	}
	form := url.Values{"username": {"admin"}, "password": {"changeme"}, "_csrf": {token}}
	signedIn := call(t, s, "POST", "/login", form.Encode(), "application/x-www-form-urlencoded", "", anonymous)
	if signedIn.Code != 303 || signedIn.Header().Get("Location") != "/dashboard" {
		t.Fatalf("login: %d %s", signedIn.Code, signedIn.Body.String())
	}
	authenticated := cookieOf(t, signedIn)
	if anonymous.Value == authenticated.Value {
		t.Fatal("session not rotated")
	}
	dashboard := call(t, s, "GET", "/dashboard", "", "", "", authenticated)
	if dashboard.Code != 200 || !strings.Contains(dashboard.Body.String(), "Панель управления") || dashboard.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("dashboard")
	}
	newToken := dashboard.Header().Get("X-CSRF-Token")
	if newToken == token {
		t.Fatal("CSRF not rotated at login")
	}
	if call(t, s, "GET", "/dashboard", "", "", "", anonymous).Code != 303 {
		t.Fatal("anonymous ID replay")
	}
	if call(t, s, "POST", "/logout", "_csrf="+token, "application/x-www-form-urlencoded", "", authenticated).Code != 403 {
		t.Fatal("stale token accepted")
	}
	if call(t, s, "GET", "/logout", "", "", "", authenticated).Code != 405 {
		t.Fatal("logout must be POST")
	}
	logout := call(t, s, "POST", "/logout", "_csrf="+newToken, "application/x-www-form-urlencoded", "", authenticated)
	if logout.Code != 303 || logout.Header().Get("Location") != "/login" {
		t.Fatal("logout")
	}
	if call(t, s, "GET", "/dashboard", "", "", "", authenticated).Code != 303 {
		t.Fatal("logged-out ID replay")
	}
	if call(t, s, "GET", "/login", "", "", "", authenticated).Code != 200 {
		t.Fatal("logout form")
	}
}

func TestLoginValidationCSRFAndFormBoundaries(t *testing.T) {
	s := testSite(t, fixture(t))
	for _, tc := range []struct {
		name, body, contentType, token string
		status                         int
	}{
		{"no token", "username=admin&password=changeme", "application/x-www-form-urlencoded", "", 403},
		{"wrong token", "username=admin&password=changeme&_csrf=bad", "application/x-www-form-urlencoded", "", 403},
		{"query credentials", "_csrf=TOKEN", "application/x-www-form-urlencoded", "", 422},
		{"empty fields", "username=&password=&_csrf=TOKEN", "application/x-www-form-urlencoded", "", 422},
		{"wrong password", "username=admin&password=wrong&_csrf=TOKEN", "application/x-www-form-urlencoded", "", 401},
		{"duplicate username", "username=admin&username=other&password=changeme&_csrf=TOKEN", "application/x-www-form-urlencoded", "", 400},
		{"unknown input", "username=admin&password=changeme&admin=true&_csrf=TOKEN", "application/x-www-form-urlencoded", "", 400},
		{"JSON removed", `{"username":"admin","password":"changeme"}`, "application/json", "TOKEN", 415},
		{"oversized body", "password=" + strings.Repeat("x", 9000), "application/x-www-form-urlencoded", "TOKEN", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := call(t, s, "GET", "/login", "", "", "", nil)
			token := initial.Header().Get("X-CSRF-Token")
			body := strings.ReplaceAll(tc.body, "TOKEN", token)
			header := strings.ReplaceAll(tc.token, "TOKEN", token)
			result := call(t, s, "POST", "/login?username=admin&password=changeme", body, tc.contentType, header, cookieOf(t, initial))
			if result.Code != tc.status {
				t.Fatalf("got %d want %d: %s", result.Code, tc.status, result.Body.String())
			}
			if strings.Contains(result.Body.String(), `value="wrong"`) {
				t.Fatal("password echoed")
			}
		})
	}
	initial := call(t, s, "GET", "/login", "", "", "", nil)
	token := initial.Header().Get("X-CSRF-Token")
	values := url.Values{"username": {"<script>alert(1)</script>"}, "password": {"wrong"}, "_csrf": {token}}
	result := call(t, s, "POST", "/login", values.Encode(), "application/x-www-form-urlencoded", "", cookieOf(t, initial))
	if strings.Contains(result.Body.String(), "<script>alert(1)</script>") {
		t.Fatal("unescaped login input")
	}
}

func TestLoginThrottle(t *testing.T) {
	s := testSite(t, fixture(t))
	initial := call(t, s, "GET", "/login", "", "", "", nil)
	cookie := cookieOf(t, initial)
	token := initial.Header().Get("X-CSRF-Token")
	for i := 0; i < 6; i++ {
		r := call(t, s, "POST", "/login", "username=admin&password=incorrect&_csrf="+token, "application/x-www-form-urlencoded", "", cookie)
		want := 401
		if i == 5 {
			want = 429
		}
		if r.Code != want {
			t.Fatalf("attempt %d: %d", i, r.Code)
		}
		if i == 5 && r.Header().Get("Retry-After") == "" {
			t.Fatal("missing Retry-After")
		}
	}
}

func TestManifestStaticFilesAndMissingBuildFailClosed(t *testing.T) {
	c := fixture(t)
	s := testSite(t, c)
	login := call(t, s, "GET", "/login", "", "", "", nil)
	for _, name := range []string{"entry-test987.js", "theme-test654.css"} {
		if !strings.Contains(login.Body.String(), "/assets/"+name) {
			t.Fatal("manifest not rendered")
		}
		r := call(t, s, "GET", "/assets/"+name, "", "", "", nil)
		if r.Code != 200 || !strings.Contains(r.Header().Get("Cache-Control"), "immutable") || r.Header().Get("Set-Cookie") != "" {
			t.Fatal("static response")
		}
	}
	for _, path := range []string{"/assets/", "/assets/.hidden", "/.vite/manifest.json", "/assets/not-found.js"} {
		if call(t, s, "GET", path, "", "", "", nil).Code != 404 {
			t.Fatalf("static disclosure: %s", path)
		}
	}
	if call(t, s, "POST", "/assets/entry-test987.js", "", "", "", nil).Code != 405 {
		t.Fatal("static method")
	}
	must(t, os.Remove(filepath.Join(c.PublicDir, "assets", "theme-test654.css")))
	if _, err := newSite(t.Context(), c); err == nil {
		t.Fatal("missing asset accepted")
	}
	c.PublicDir = t.TempDir()
	if _, err := newSite(t.Context(), c); err == nil || !strings.Contains(err.Error(), "build required") {
		t.Fatal("missing build accepted")
	}
}

func TestProductionConfigurationCookiesAndNoFallback(t *testing.T) {
	values := map[string]string{"APP_ENV": "production", "BASIC_USERNAME": "owner", "BASIC_ALLOWED_HOSTS": "localhost:8080", "BASIC_PASSWORD_HASH_FILE": filepath.Join(t.TempDir(), "password.phc")}
	hasher, err := auth.NewPasswordHasher(auth.PasswordConfig{})
	must(t, err)
	encoded, err := hasher.Hash(t.Context(), "configured-password")
	must(t, err)
	must(t, os.WriteFile(values["BASIC_PASSWORD_HASH_FILE"], []byte(encoded), 0600))
	for _, key := range []string{"BASIC_USERNAME", "BASIC_ALLOWED_HOSTS", "BASIC_PASSWORD_HASH_FILE"} {
		old := values[key]
		delete(values, key)
		if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
		values[key] = old
	}
	c, err := loadConfig(func(key string) string { return values[key] })
	must(t, err)
	c.PublicDir = fixture(t).PublicDir
	s := testSite(t, c)
	r := call(t, s, "GET", "/login", "", "", "", nil)
	cookie := cookieOf(t, r)
	if !cookie.Secure || cookie.Name != "__Host-basic_session" || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatal("production cookie")
	}
	if strings.Contains(r.Body.String(), "changeme") || r.Header().Get("Strict-Transport-Security") == "" || !strings.Contains(r.Header().Get("Content-Security-Policy"), "form-action 'self'") {
		t.Fatal("production page policy")
	}
	wrongHost := httptest.NewRequest("GET", "https://attacker.invalid/login", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, wrongHost)
	if w.Code == 200 {
		t.Fatal("unapproved host")
	}
	token := r.Header().Get("X-CSRF-Token")
	if call(t, s, "POST", "/login", "username=owner&password=changeme&_csrf="+token, "application/x-www-form-urlencoded", "", cookie).Code != 401 {
		t.Fatal("production fallback")
	}
	if call(t, s, "POST", "/login", "username=owner&password=configured-password&_csrf="+token, "application/x-www-form-urlencoded", "", cookie).Code != 303 {
		t.Fatal("configured account rejected")
	}
	c.PasswordHash = "invalid"
	if _, err := newSite(t.Context(), c); err == nil {
		t.Fatal("invalid hash accepted")
	}
}

func TestConfigAndPasswordHashUtility(t *testing.T) {
	for _, values := range []map[string]string{{"APP_ENV": "staging"}, {"BASIC_ADDRESS": "bad"}, {"BASIC_TRUSTED_PROXIES": "bad"}, {"BASIC_USERNAME": " space "}} {
		if _, err := loadConfig(func(key string) string { return values[key] }); err == nil {
			t.Fatal("invalid configuration")
		}
	}
	var output bytes.Buffer
	must(t, hashPassword(strings.NewReader("configured-password\n"), &output))
	h, err := auth.NewPasswordHasher(auth.PasswordConfig{})
	must(t, err)
	_, err = h.Verify(context.Background(), "configured-password", strings.TrimSpace(output.String()))
	must(t, err)
	if hashPassword(strings.NewReader("short"), io.Discard) == nil {
		t.Fatal("weak password accepted")
	}
}
