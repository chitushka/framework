package session

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func FuzzCookie(f *testing.F) {
	store, err := NewCookieStore(bytes.Repeat([]byte{1}, 32), Config{})
	if err != nil {
		f.Fatal(err)
	}
	store.now = func() time.Time { return time.Unix(1800000000, 0) }
	current, err := store.load(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		f.Fatal(err)
	}
	if err := current.Set("role", "admin"); err != nil {
		f.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := store.commit(response, current); err != nil {
		f.Fatal(err)
	}
	for _, value := range []string{response.Result().Cookies()[0].Value, "", "v1.primary.!", "v1.unknown.AAAA", "v2.primary.AAAA", strings.Repeat("a", 3801)} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 8192 {
			t.Skip()
		}
		load := func(value string) *Session {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Cookie", store.config.Name+"="+value)
			current, err := store.load(request)
			if err != nil || current == nil || !validID(current.ID()) {
				t.Fatalf("cookie load failed to produce a valid session: %v", err)
			}
			return current
		}
		current := load(value)
		if !current.loaded {
			if len(current.values) != 0 {
				t.Fatal("invalid cookie supplied session state")
			}
			return
		}
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Cookie", store.config.Name+"="+value)
		cookie, err := request.Cookie(store.config.Name)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(cookie.Value, ".")
		if len(parts) != 3 {
			t.Fatal("accepted a malformed cookie envelope")
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || len(payload) == 0 {
			t.Fatal("accepted invalid ciphertext encoding")
		}
		payload[len(payload)-1] ^= 1
		if load(parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(payload)).loaded {
			t.Fatal("cookie authentication accepted corrupted ciphertext")
		}
	})
}
