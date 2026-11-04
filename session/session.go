// Package session provides server-side HTTP sessions with replaceable storage.
package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chitushka/framework/internal/httpwriter"
	"github.com/chitushka/framework/web"
)

// Backend stores serialized session values. Implementations must be safe for concurrent use.
type Backend interface {
	Load(context.Context, string) ([]byte, bool, error)
	Save(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

// AtomicBackend must compare the exact loaded bytes and replace, rotate or delete
// a record in one atomic operation shared by ALL application instances. A nil
// expected value means the old ID must be absent; an empty newID deletes it.
// Rotation must also reject an already occupied newID. Expired records are absent.
// Save/Delete and CompareAndSwap must participate in the same serialization order.
type AtomicBackend interface {
	Backend
	CompareAndSwap(ctx context.Context, oldID string, expected []byte, newID string, value []byte, ttl time.Duration) (bool, error)
}

var (
	ErrConflict  = errors.New("session: concurrent update or revoked session")
	ErrCommitted = errors.New("session: response has already committed the session")
)

// KeyValueBackend is implemented by the framework cache backends, including Redis and Database.
type KeyValueBackend interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

// AtomicKeyValueBackend extends a cache backend with AtomicBackend's compare,
// expiration and cross-key rotation guarantees. Standard cache adapters do not
// currently implement this interface.
type AtomicKeyValueBackend interface {
	KeyValueBackend
	CompareAndSwap(context.Context, string, []byte, string, []byte, time.Duration) (bool, error)
}

type keyValueAdapter struct{ backend AtomicKeyValueBackend }

// NewBackend adapts a cache backend that also implements atomic CompareAndSwap
// with the AtomicBackend semantics (including cross-key rotation). Plain cache
// Get/Set/Delete implementations are rejected; they cannot prevent resurrection.
func NewBackend(backend KeyValueBackend) (Backend, error) {
	if backend == nil {
		return nil, errors.New("session: key-value backend is required")
	}
	atomic, ok := backend.(AtomicKeyValueBackend)
	if !ok {
		return nil, errors.New("session: cache backend requires atomic CompareAndSwap including rotation")
	}
	return &keyValueAdapter{backend: atomic}, nil
}

func (a *keyValueAdapter) CompareAndSwap(ctx context.Context, oldID string, expected []byte, newID string, value []byte, ttl time.Duration) (bool, error) {
	next := ""
	if newID != "" {
		next = "session:" + newID
	}
	return a.backend.CompareAndSwap(ctx, "session:"+oldID, expected, next, value, ttl)
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
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.entries[id]
	if !ok {
		return nil, false, nil
	}
	if !entry.expires.IsZero() && !b.now().Before(entry.expires) {
		delete(b.entries, id)
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (b *MemoryBackend) CompareAndSwap(ctx context.Context, oldID string, expected []byte, newID string, value []byte, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for _, id := range []string{oldID, newID} {
		if entry, ok := b.entries[id]; ok && !entry.expires.IsZero() && !now.Before(entry.expires) {
			delete(b.entries, id)
		}
	}
	entry, exists := b.entries[oldID]
	if (expected == nil && exists) || (expected != nil && (!exists || !bytes.Equal(entry.value, expected))) {
		return false, nil
	}
	if newID != "" && newID != oldID {
		if _, exists := b.entries[newID]; exists {
			return false, nil
		}
	}
	delete(b.entries, oldID)
	if newID != "" {
		entry = memoryEntry{value: append([]byte(nil), value...)}
		if ttl > 0 {
			entry.expires = now.Add(ttl)
		}
		b.entries[newID] = entry
	}
	return true, nil
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
	Name string
	TTL  time.Duration
	// AbsoluteTTL bounds the complete session lifetime, including regenerated IDs.
	// TTL is the idle lifetime. Defaults: 24 hours idle, 7 days absolute.
	AbsoluteTTL time.Duration
	Path        string
	Domain      string
	Secure      bool
	SameSite    http.SameSite
}

// Manager loads and atomically persists a Session for every request. Concurrent
// commits from the same snapshot are rejected with HTTP 409 and ErrConflict.
// This does not roll back application side effects performed by the handler.
type Manager struct {
	backend AtomicBackend
	config  Config
	now     func() time.Time
}

func New(backend Backend, config Config) (*Manager, error) {
	if backend == nil {
		return nil, errors.New("session: backend is required")
	}
	atomic, ok := backend.(AtomicBackend)
	if !ok {
		return nil, errors.New("session: backend must implement AtomicBackend")
	}
	config, err := normalizedConfig(config)
	if err != nil {
		return nil, err
	}
	return &Manager{backend: atomic, config: config, now: time.Now}, nil
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
	if config.AbsoluteTTL == 0 {
		config.AbsoluteTTL = 7 * 24 * time.Hour
	}
	if config.TTL < time.Second || config.AbsoluteTTL < time.Second {
		return Config{}, errors.New("session: idle and absolute TTL must be at least one second")
	}
	if strings.ContainsAny(config.Name, "=;, \t\r\n") {
		return Config{}, errors.New("session: invalid cookie name")
	}
	if config.SameSite == 0 {
		config.SameSite = http.SameSiteLaxMode
	}
	if config.SameSite < http.SameSiteDefaultMode || config.SameSite > http.SameSiteNoneMode ||
		(config.SameSite == http.SameSiteNoneMode && !config.Secure) ||
		(strings.HasPrefix(config.Name, "__Secure-") && !config.Secure) ||
		(strings.HasPrefix(config.Name, "__Host-") && (!config.Secure || config.Path != "/" || config.Domain != "")) {
		return Config{}, errors.New("session: invalid secure cookie policy")
	}
	if err := cookieFromConfig(config, "", 1).Valid(); err != nil {
		return Config{}, fmt.Errorf("session: cookie policy: %w", err)
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
	committed bool
	expected  []byte
	created   time.Time
	touched   time.Time
	loaded    bool
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
	if s.committed {
		return ErrCommitted
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
	if s.committed || s.destroyed {
		return
	}
	if _, ok := s.values[key]; ok {
		delete(s.values, key)
		s.dirty = true
	}
}

// Regenerate atomically replaces the identifier on commit, invalidates the old
// ID, and clears the CSRF token. It preserves the original absolute lifetime.
func (s *Session) Regenerate() error {
	id, err := randomID()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committed || s.destroyed {
		return ErrCommitted
	}
	s.id = id
	// Authentication transitions must not retain the anonymous CSRF token.
	delete(s.values, "security.csrf")
	s.dirty = true
	return nil
}

// Destroy removes the session and expires its cookie.
func (s *Session) Destroy() {
	s.mu.Lock()
	if s.committed {
		s.mu.Unlock()
		return
	}
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
		ctx.Writer = httpwriter.Preserve(writer)
		defer func() { ctx.Writer = originalWriter }()
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
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.ensureCommitted() == nil {
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *commitWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *commitWriter) FlushError() error {
	if err := w.ensureCommitted(); err != nil {
		return err
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *commitWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if err := w.ensureCommitted(); err != nil {
		return nil, nil, err
	}
	return http.NewResponseController(w.ResponseWriter).Hijack()
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
	if id != "" {
		payload, found, err := m.backend.Load(request.Context(), id)
		if err != nil {
			return nil, "", fmt.Errorf("session: load: %w", err)
		}
		var record sessionRecord
		if found && json.Unmarshal(payload, &record) == nil && record.valid(m.now(), m.config) && record.ID == id {
			return &Session{id: id, values: record.Values, expected: payload, created: record.Created, touched: record.Touched, loaded: true}, id, nil
		}
	}
	// Never reuse an unknown, expired or malformed identifier supplied by a client.
	id, err := randomID()
	if err != nil {
		return nil, "", err
	}
	return &Session{id: id, values: make(map[string]json.RawMessage), created: m.now()}, id, nil
}

func (m *Manager) commit(ctx context.Context, writer http.ResponseWriter, s *Session, oldID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committed = true
	id, dirty, destroyed := s.id, s.dirty, s.destroyed
	if destroyed {
		if err := m.backend.Delete(ctx, oldID); err != nil {
			return fmt.Errorf("session: destroy: %w", err)
		}
		http.SetCookie(writer, m.cookie("", -1))
		return nil
	}
	if !dirty && !s.loaded {
		return nil
	}
	now := m.now()
	ttl := min(m.config.TTL, s.created.Add(m.config.AbsoluteTTL).Sub(now))
	if ttl <= 0 || (s.loaded && !now.Before(s.touched.Add(m.config.TTL))) {
		return web.NewHTTPError(http.StatusConflict, "session expired", ErrConflict)
	}
	revision, err := randomID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sessionRecord{Version: 1, ID: id, Revision: revision, Created: s.created, Touched: now, Values: s.values})
	if err != nil {
		return fmt.Errorf("session: encode: %w", err)
	}
	swapped, err := m.backend.CompareAndSwap(ctx, oldID, s.expected, id, payload, ttl)
	if err != nil {
		return fmt.Errorf("session: save: %w", err)
	}
	if !swapped {
		return web.NewHTTPError(http.StatusConflict, "session changed; reload before retrying", ErrConflict)
	}
	http.SetCookie(writer, m.cookie(id, cookieAge(ttl)))
	s.dirty = false
	return nil
}

// Versioned envelopes deliberately invalidate the old unbounded snapshot format.
type sessionRecord struct {
	Version  int                        `json:"v"`
	ID       string                     `json:"id"`
	Revision string                     `json:"revision"`
	Created  time.Time                  `json:"created"`
	Touched  time.Time                  `json:"touched"`
	Values   map[string]json.RawMessage `json:"values"`
}

func (r sessionRecord) valid(now time.Time, config Config) bool {
	return r.Version == 1 && validID(r.ID) && validID(r.Revision) && r.Values != nil &&
		!r.Created.IsZero() && !r.Touched.Before(r.Created) && !now.Before(r.Touched) &&
		now.Before(r.Created.Add(config.AbsoluteTTL)) && now.Before(r.Touched.Add(config.TTL))
}

func cookieAge(ttl time.Duration) int {
	// Never round a positive sub-second remainder to MaxAge=0 (session cookie).
	return max(1, int(ttl/time.Second))
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
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}
