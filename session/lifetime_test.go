package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func loadSession(t *testing.T, manager *Manager, cookie *http.Cookie) (*Session, string) {
	t.Helper()
	request := httptest.NewRequest("GET", "/", nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	current, id, err := manager.load(request)
	if err != nil {
		t.Fatal(err)
	}
	return current, id
}

func commitSession(t *testing.T, manager *Manager, current *Session, id string) *http.Cookie {
	t.Helper()
	response := httptest.NewRecorder()
	if err := manager.commit(t.Context(), response, current, id); err != nil {
		t.Fatal(err)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	return cookies[0]
}

func TestAtomicUpdatesRotationAndRevocationAcrossManagers(t *testing.T) {
	for _, transition := range []string{"update", "regenerate", "destroy"} {
		t.Run(transition, func(t *testing.T) {
			backend := NewMemoryBackend()
			first, _ := New(backend, Config{})
			second, _ := New(backend, Config{})
			initial, id := loadSession(t, first, nil)
			if err := initial.Set("role", "member"); err != nil {
				t.Fatal(err)
			}
			cookie := commitSession(t, first, initial, id)
			winner, winnerID := loadSession(t, first, cookie)
			stale, staleID := loadSession(t, second, cookie)
			if err := stale.Set("role", "stale"); err != nil {
				t.Fatal(err)
			}
			switch transition {
			case "update":
				if err := winner.Set("role", "winner"); err != nil {
					t.Fatal(err)
				}
			case "regenerate":
				if err := winner.Regenerate(); err != nil {
					t.Fatal(err)
				}
			case "destroy":
				winner.Destroy()
			}
			next := commitSession(t, first, winner, winnerID)
			response := httptest.NewRecorder()
			if err := second.commit(t.Context(), response, stale, staleID); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale writer accepted: %v", err)
			}
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("conflict changed cookie")
			}
			if transition != "update" {
				replayed, _ := loadSession(t, second, cookie)
				var role string
				if found, err := replayed.Get("role", &role); err != nil || found || replayed.ID() == cookie.Value {
					t.Fatal("old ID replay restored revoked session")
				}
			}
			if transition == "regenerate" {
				restored, _ := loadSession(t, second, next)
				var role string
				if found, err := restored.Get("role", &role); err != nil || !found || role != "member" {
					t.Fatal("rotation lost values")
				}
			}
		})
	}
}

func TestIdleAndAbsoluteLifetimeWithReadOnlyRenewal(t *testing.T) {
	backend := NewMemoryBackend()
	manager, _ := New(backend, Config{TTL: 10 * time.Second, AbsoluteTTL: 25 * time.Second})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	backend.now = manager.now
	initial, id := loadSession(t, manager, nil)
	if err := initial.Set("user", "alice"); err != nil {
		t.Fatal(err)
	}
	cookie := commitSession(t, manager, initial, id)
	for _, elapsed := range []time.Duration{9, 18, 24} {
		now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(elapsed * time.Second)
		current, id := loadSession(t, manager, cookie)
		if !current.loaded {
			t.Fatal("read-only renewal expired prematurely")
		}
		cookie = commitSession(t, manager, current, id)
	}
	if cookie.MaxAge != 1 {
		t.Fatalf("cookie outlives absolute TTL: %d", cookie.MaxAge)
	}
	now = now.Add(time.Second)
	expired, _ := loadSession(t, manager, cookie)
	if expired.loaded || expired.ID() == cookie.Value {
		t.Fatal("absolute expiry accepted")
	}
	initial, id = loadSession(t, manager, nil)
	if err := initial.Set("user", "bob"); err != nil {
		t.Fatal(err)
	}
	cookie = commitSession(t, manager, initial, id)
	now = now.Add(10 * time.Second)
	expired, _ = loadSession(t, manager, cookie)
	if expired.loaded {
		t.Fatal("idle expiry accepted")
	}
}

func TestCommitExpiryFreezeAndUnknownIdentifier(t *testing.T) {
	backend := NewMemoryBackend()
	manager, _ := New(backend, Config{TTL: time.Second, AbsoluteTTL: time.Second})
	now := time.Now()
	manager.now = func() time.Time { return now }
	backend.now = manager.now
	unknown, _ := randomID()
	current, id := loadSession(t, manager, &http.Cookie{Name: manager.config.Name, Value: unknown})
	if current.ID() == unknown {
		t.Fatal("adopted attacker supplied ID")
	}
	if err := current.Set("x", 1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := manager.commit(t.Context(), httptest.NewRecorder(), current, id); !errors.Is(err, ErrConflict) {
		t.Fatalf("expiry during handler: %v", err)
	}
	if err := current.Set("x", 2); !errors.Is(err, ErrCommitted) {
		t.Fatal(err)
	}
	if err := current.Regenerate(); !errors.Is(err, ErrCommitted) {
		t.Fatal(err)
	}
	current.Delete("x")
	current.Destroy()
	var value int
	if _, err := current.Get("x", &value); err != nil || value != 1 {
		t.Fatal("post-commit mutation")
	}
}

func TestCookieAbsoluteLifetimeSurvivesRenewalAndRegeneration(t *testing.T) {
	store, err := NewCookieStore([]byte(strings.Repeat("k", 32)), Config{TTL: 10 * time.Second, AbsoluteTTL: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	now := start
	store.now = func() time.Time { return now }
	request := httptest.NewRequest("GET", "/", nil)
	current, err := store.load(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Set("user", "alice"); err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, elapsed := range []time.Duration{0, 9, 18, 24} {
		now = start.Add(elapsed * time.Second)
		if cookie != nil {
			request = httptest.NewRequest("GET", "/", nil)
			request.AddCookie(cookie)
			current, err = store.load(request)
			if err != nil || !current.loaded {
				t.Fatal("premature expiry", err)
			}
		}
		if err := current.Regenerate(); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		if err := store.commit(response, current); err != nil {
			t.Fatal(err)
		}
		cookie = response.Result().Cookies()[0]
	}
	if cookie.MaxAge != 1 {
		t.Fatal("absolute lifetime reset on regeneration")
	}
	now = start.Add(25 * time.Second)
	request = httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	current, err = store.load(request)
	if err != nil || current.loaded {
		t.Fatal("cookie accepted at absolute expiry", err)
	}
}

type legacyBackend struct{ Backend }
type plainCache struct{}

func (plainCache) Get(context.Context, string) ([]byte, bool, error)        { return nil, false, nil }
func (plainCache) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (plainCache) Delete(context.Context, string) error                     { return nil }

func TestNonAtomicBackendsRejectedAndCancellationPropagates(t *testing.T) {
	if _, err := New(legacyBackend{NewMemoryBackend()}, Config{}); err == nil {
		t.Fatal("non-atomic backend accepted")
	}
	if _, err := NewBackend(plainCache{}); err == nil {
		t.Fatal("non-atomic cache adapter accepted")
	}
	backend := NewMemoryBackend()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := backend.CompareAndSwap(ctx, "old", nil, "new", []byte("value"), time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, ok, err := backend.Load(t.Context(), "new"); err != nil || ok {
		t.Fatal("canceled CAS wrote data")
	}
}

func TestAtomicBackendRotationDoesNotOverwriteOccupiedID(t *testing.T) {
	backend := NewMemoryBackend()
	if err := backend.Save(t.Context(), "old", []byte("original"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := backend.Save(t.Context(), "new", []byte("occupied"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if ok, err := backend.CompareAndSwap(t.Context(), "old", []byte("original"), "new", []byte("replacement"), time.Minute); err != nil || ok {
		t.Fatalf("occupied ID overwritten: %v %v", ok, err)
	}
	for id, want := range map[string]string{"old": "original", "new": "occupied"} {
		got, ok, err := backend.Load(t.Context(), id)
		if err != nil || !ok || string(got) != want {
			t.Fatal("failed rotation partially changed state")
		}
	}
	if ok, err := backend.CompareAndSwap(t.Context(), "old", nil, "old", []byte("replacement"), time.Minute); err != nil || ok {
		t.Fatal("create overwrote existing record")
	}
	if ok, err := backend.CompareAndSwap(t.Context(), "old", []byte("original"), "", nil, 0); err != nil || !ok {
		t.Fatal("atomic delete failed")
	}
	if _, ok, err := backend.Load(t.Context(), "old"); err != nil || ok {
		t.Fatal("atomic delete retained record")
	}
}

func TestCookieKeyRotationExpiryAndBounds(t *testing.T) {
	now := time.Now().UTC()
	oldKey := CookieKey{ID: "old", Secret: []byte(strings.Repeat("a", 32))}
	newKey := CookieKey{ID: "new", Secret: []byte(strings.Repeat("b", 32))}
	config := Config{TTL: 10 * time.Second, AbsoluteTTL: 25 * time.Second}
	old, err := NewCookieStoreWithKeys([]CookieKey{oldKey}, config)
	if err != nil {
		t.Fatal(err)
	}
	old.now = func() time.Time { return now }
	current, err := old.load(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Set("role", "member"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	if err := old.commit(response, current); err != nil {
		t.Fatal(err)
	}
	cookie := response.Result().Cookies()[0]
	oldKey.AcceptUntil = now.Add(5 * time.Second)
	rotated, err := NewCookieStoreWithKeys([]CookieKey{newKey, oldKey}, config)
	if err != nil {
		t.Fatal(err)
	}
	rotated.now = func() time.Time { return now }
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(cookie)
	loaded, err := rotated.load(request)
	if err != nil || !loaded.loaded || !loaded.dirty {
		t.Fatalf("old key not accepted: %v", err)
	}
	response = httptest.NewRecorder()
	if err := rotated.commit(response, loaded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.Result().Cookies()[0].Value, "v1.new.") {
		t.Fatal("not reissued under active key")
	}
	now = now.Add(5 * time.Second)
	loaded, err = rotated.load(request)
	if err != nil || loaded.loaded {
		t.Fatal("old key accepted after grace deadline")
	}
	now = now.Add(5 * time.Second)
	loaded, err = old.load(request)
	if err != nil || loaded.loaded {
		t.Fatal("server accepted expired cookie")
	}
	request = httptest.NewRequest("GET", "/", nil)
	loaded, _ = rotated.load(request)
	if err := loaded.Set("large", strings.Repeat("x", 4096)); err != nil {
		t.Fatal(err)
	}
	if err := rotated.commit(httptest.NewRecorder(), loaded); err == nil {
		t.Fatal("oversized cookie accepted")
	}
	for _, keys := range [][]CookieKey{nil, {newKey, newKey}, {newKey, {ID: "old", Secret: oldKey.Secret}}, {{ID: "invalid.id", Secret: oldKey.Secret}}, {{ID: "short", Secret: []byte("short")}}} {
		if _, err := NewCookieStoreWithKeys(keys, config); err == nil {
			t.Fatalf("invalid keys accepted: %v", keys)
		}
	}
}

type brokenBackend struct{ err error }

func (b brokenBackend) Load(context.Context, string) ([]byte, bool, error)        { return nil, false, b.err }
func (b brokenBackend) Save(context.Context, string, []byte, time.Duration) error { return b.err }
func (b brokenBackend) Delete(context.Context, string) error                      { return b.err }
func (b brokenBackend) CompareAndSwap(context.Context, string, []byte, string, []byte, time.Duration) (bool, error) {
	return false, b.err
}

func TestBackendFailuresAndCookiePolicy(t *testing.T) {
	want := errors.New("backend unavailable")
	manager, _ := New(brokenBackend{want}, Config{})
	id, _ := randomID()
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: manager.config.Name, Value: id})
	if _, _, err := manager.load(request); !errors.Is(err, want) {
		t.Fatal(err)
	}
	for _, destroy := range []bool{false, true} {
		current := &Session{id: id, values: map[string]json.RawMessage{}, dirty: true, destroyed: destroy, created: time.Now()}
		if err := manager.commit(t.Context(), httptest.NewRecorder(), current, id); !errors.Is(err, want) {
			t.Fatal(err)
		}
	}
	for _, config := range []Config{{TTL: -1}, {AbsoluteTTL: -1}, {TTL: time.Nanosecond}, {Name: "__Host-sid"}, {Name: "__Secure-sid"}, {SameSite: http.SameSiteNoneMode}, {Secure: true, Name: "__Host-sid", Domain: "example.com"}, {Path: "bad;path"}, {Name: "bad(name)"}} {
		if _, err := New(NewMemoryBackend(), config); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
	if _, err := New(NewMemoryBackend(), Config{Name: "__Host-sid", Secure: true}); err != nil {
		t.Fatal(err)
	}
}
