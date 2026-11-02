// Package session provides server-side HTTP sessions with replaceable storage.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chitushka/framework/web"
)

// Backend stores serialized session values. Implementations must be safe for concurrent use.
type Backend interface {
	Load(context.Context, string) ([]byte, bool, error)
	Save(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

// KeyValueBackend is implemented by the framework cache backends, including Redis and Database.
type KeyValueBackend interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

type keyValueAdapter struct{ backend KeyValueBackend }

// NewBackend adapts a cache-compatible Redis, Database, Memcached or memory backend.
func NewBackend(backend KeyValueBackend) (Backend, error) {
	if backend == nil {
		return nil, errors.New("session: key-value backend is required")
	}
	return &keyValueAdapter{backend: backend}, nil
}

func (a *keyValueAdapter) Load(ctx context.Context, id string) ([]byte, bool, error) {
	return a.backend.Get(ctx, "session:"+id)
}
func (a *keyValueAdapter) Save(ctx context.Context, id string, value []byte, ttl time.Duration) error {
	return a.backend.Set(ctx, "session:"+id, value, ttl)
}
func (a *keyValueAdapter) Delete(ctx context.Context, id string) error {
	return a.backend.Delete(ctx, "session:"+id)
}

type memoryEntry struct {
	value   []byte
	expires time.Time
}

// MemoryBackend is a concurrency-safe in-process session backend.
type MemoryBackend struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
	now     func() time.Time
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{entries: make(map[string]memoryEntry), now: time.Now}
}

func (b *MemoryBackend) Load(ctx context.Context, id string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	b.mu.RLock()
	entry, ok := b.entries[id]
	b.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if !entry.expires.IsZero() && !b.now().Before(entry.expires) {
		b.mu.Lock()
		delete(b.entries, id)
		b.mu.Unlock()
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}
func (b *MemoryBackend) Save(ctx context.Context, id string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := memoryEntry{value: append([]byte(nil), value...)}
	if ttl > 0 {
		entry.expires = b.now().Add(ttl)
	}
	b.mu.Lock()
	b.entries[id] = entry
	b.mu.Unlock()
	return nil
}
func (b *MemoryBackend) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.entries, id)
	b.mu.Unlock()
	return nil
}

// Config controls session lifetime and cookie security attributes.
type Config struct {
	Name     string
	TTL      time.Duration
	Path     string
	Domain   string
	Secure   bool
	SameSite http.SameSite
}

// Manager loads and persists a Session for every request.
type Manager struct {
	backend Backend
	config  Config
}

func New(backend Backend, config Config) (*Manager, error) {
	if backend == nil {
		return nil, errors.New("session: backend is required")
	}
	config, err := normalizedConfig(config)
	if err != nil {
		return nil, err
	}
	return &Manager{backend: backend, config: config}, nil
}

func normalizedConfig(config Config) (Config, error) {
	if config.Name == "" {
		config.Name = "chitu_session"
	}
	if config.Path == "" {
		config.Path = "/"
	}
	if config.TTL == 0 {
		config.TTL = 24 * time.Hour
	}
	if config.TTL < 0 {
		return Config{}, errors.New("session: TTL cannot be negative")
	}
	if strings.ContainsAny(config.Name, "=;, \t\r\n") {
		return Config{}, errors.New("session: invalid cookie name")
	}
	if config.SameSite == 0 {
		config.SameSite = http.SameSiteLaxMode
	}
	return config, nil
}

type contextKey struct{}

// Session is request-scoped. Values must be JSON serializable.
type Session struct {
	mu        sync.RWMutex
	id        string
	values    map[string]json.RawMessage
	dirty     bool
	destroyed bool
}

func FromContext(ctx context.Context) (*Session, bool) {
	value, ok := ctx.Value(contextKey{}).(*Session)
	return value, ok
}

func Must(ctx context.Context) *Session {
	value, ok := FromContext(ctx)
	if !ok {
		panic("session: middleware is not installed")
	}
	return value
}

func (s *Session) ID() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.id }

func (s *Session) Set(key string, value any) error {
	if key == "" {
		return errors.New("session: key is empty")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("session: encode %q: %w", key, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return errors.New("session: session is destroyed")
	}
	s.values[key] = payload
	s.dirty = true
	return nil
}

func (s *Session) Get(key string, target any) (bool, error) {
	if target == nil {
		return false, errors.New("session: target is nil")
	}
	s.mu.RLock()
	payload, ok := s.values[key]
	payload = append([]byte(nil), payload...)
	s.mu.RUnlock()
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return false, fmt.Errorf("session: decode %q: %w", key, err)
	}
	return true, nil
}

func (s *Session) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[key]; ok {
		delete(s.values, key)
		s.dirty = true
	}
}

// Regenerate replaces the identifier on commit and invalidates the previous one.
func (s *Session) Regenerate() error {
	id, err := randomID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.id = id
	s.dirty = true
	s.mu.Unlock()
	return nil
}

// Destroy removes the session and expires its cookie.
func (s *Session) Destroy() {
	s.mu.Lock()
	s.values = make(map[string]json.RawMessage)
	s.destroyed = true
	s.dirty = true
	s.mu.Unlock()
}

func (m *Manager) Middleware(next web.Handler) web.Handler {
	return func(ctx *web.Context) error {
		session, oldID, err := m.load(ctx.Request)
		if err != nil {
			return err
		}
		ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), contextKey{}, session))
		originalWriter := ctx.Writer
		writer := &commitWriter{ResponseWriter: originalWriter, commit: func() error {
			return m.commit(ctx.Request.Context(), originalWriter, session, oldID)
		}}
		ctx.Writer = writer
		err = next(ctx)
		commitErr := writer.ensureCommitted()
		if err != nil && commitErr != nil {
			return errors.Join(err, commitErr)
		}
		if err != nil {
			return err
		}
		return commitErr
	}
}

type commitWriter struct {
	http.ResponseWriter
	commit func() error
	once   sync.Once
	err    error
}

func (w *commitWriter) ensureCommitted() error {
	w.once.Do(func() { w.err = w.commit() })
	return w.err
}
func (w *commitWriter) WriteHeader(status int) {
	if w.ensureCommitted() == nil {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *commitWriter) Write(payload []byte) (int, error) {
	if err := w.ensureCommitted(); err != nil {
		return 0, err
	}
	return w.ResponseWriter.Write(payload)
}

func (m *Manager) load(request *http.Request) (*Session, string, error) {
	id := ""
	if cookie, err := request.Cookie(m.config.Name); err == nil {
		id = cookie.Value
	}
	if !validID(id) {
		id = ""
	}
	values := make(map[string]json.RawMessage)
	if id != "" {
		payload, found, err := m.backend.Load(request.Context(), id)
		if err != nil {
			return nil, "", fmt.Errorf("session: load: %w", err)
		}
		if found && json.Unmarshal(payload, &values) != nil {
			_ = m.backend.Delete(request.Context(), id)
			values = make(map[string]json.RawMessage)
			id = ""
		}
	}
	if id == "" {
		var err error
		id, err = randomID()
		if err != nil {
			return nil, "", err
		}
	}
	return &Session{id: id, values: values}, id, nil
}

func (m *Manager) commit(ctx context.Context, writer http.ResponseWriter, s *Session, oldID string) error {
	s.mu.RLock()
	id, dirty, destroyed := s.id, s.dirty, s.destroyed
	values := make(map[string]json.RawMessage, len(s.values))
	for key, value := range s.values {
		values[key] = append([]byte(nil), value...)
	}
	s.mu.RUnlock()
	if destroyed {
		if err := m.backend.Delete(ctx, oldID); err != nil {
			return fmt.Errorf("session: destroy: %w", err)
		}
		http.SetCookie(writer, m.cookie("", -1))
		return nil
	}
	if !dirty {
		return nil
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("session: encode: %w", err)
	}
	if err := m.backend.Save(ctx, id, payload, m.config.TTL); err != nil {
		return fmt.Errorf("session: save: %w", err)
	}
	if oldID != id {
		if err := m.backend.Delete(ctx, oldID); err != nil {
			return fmt.Errorf("session: delete old identifier: %w", err)
		}
	}
	http.SetCookie(writer, m.cookie(id, int(m.config.TTL.Seconds())))
	s.mu.Lock()
	s.dirty = false
	s.mu.Unlock()
	return nil
}

func (m *Manager) cookie(value string, maxAge int) *http.Cookie {
	return cookieFromConfig(m.config, value, maxAge)
}

func cookieFromConfig(config Config, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: config.Name, Value: value, Path: config.Path, Domain: config.Domain, MaxAge: maxAge, HttpOnly: true, Secure: config.Secure, SameSite: config.SameSite}
}

func randomID() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("session: generate identifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}
func validID(value string) bool {
	if len(value) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}
