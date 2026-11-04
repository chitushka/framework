package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chitushka/framework/internal/httpwriter"
	"github.com/chitushka/framework/web"
)

// CookieStore keeps the complete session in an authenticated, encrypted cookie.
// It is suitable for small non-critical state only: concurrent responses use
// last-cookie-wins and individual cookies cannot be revoked before expiry.
// Use Manager with AtomicBackend for authentication requiring logout/revocation.
type CookieStore struct {
	keys   []cookieCipher
	config Config
	now    func() time.Time
}

// CookieKey identifies an encryption key. The first key issues cookies; previous
// keys only decrypt until AcceptUntil, which is mandatory for previous keys.
type CookieKey struct {
	ID          string
	Secret      []byte
	AcceptUntil time.Time
}

type cookieCipher struct {
	id          string
	aead        cipher.AEAD
	acceptUntil time.Time
}

// NewCookieStore creates stateless cookie storage. Secret must contain at least
// 32 random bytes and must remain stable across application instances and restarts.
func NewCookieStore(secret []byte, config Config) (*CookieStore, error) {
	return NewCookieStoreWithKeys([]CookieKey{{ID: "primary", Secret: secret}}, config)
}

// NewCookieStoreWithKeys accepts at most four keys. Deploy the same ordered ring
// on every instance; remove previous keys after their explicit grace deadline.
func NewCookieStoreWithKeys(keys []CookieKey, config Config) (*CookieStore, error) {
	if len(keys) == 0 || len(keys) > 4 {
		return nil, errors.New("session: cookie key ring must contain 1 to 4 keys")
	}
	validated, err := normalizedConfig(config)
	if err != nil {
		return nil, err
	}
	store := &CookieStore{config: validated, now: time.Now}
	seen := make(map[string]bool)
	for i, entry := range keys {
		if len(entry.Secret) < 32 || entry.ID == "" || len(entry.ID) > 32 || seen[entry.ID] ||
			strings.Trim(entry.ID, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" ||
			(i > 0 && entry.AcceptUntil.IsZero()) || (!entry.AcceptUntil.IsZero() && !time.Now().Before(entry.AcceptUntil)) {
			return nil, errors.New("session: invalid cookie key or grace deadline")
		}
		seen[entry.ID] = true
		key := sha256.Sum256(entry.Secret)
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return nil, fmt.Errorf("session: create cookie cipher: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("session: create cookie AEAD: %w", err)
		}
		store.keys = append(store.keys, cookieCipher{id: entry.ID, aead: aead, acceptUntil: entry.AcceptUntil})
	}
	return store, nil
}

// Middleware loads and commits encrypted cookie sessions.
func (store *CookieStore) Middleware(next web.Handler) web.Handler {
	return func(ctx *web.Context) error {
		current, err := store.load(ctx.Request)
		if err != nil {
			return err
		}
		ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), contextKey{}, current))
		originalWriter := ctx.Writer
		writer := &commitWriter{ResponseWriter: originalWriter, commit: func() error {
			return store.commit(originalWriter, current)
		}}
		ctx.Writer = httpwriter.Preserve(writer)
		defer func() { ctx.Writer = originalWriter }()
		handlerErr := next(ctx)
		commitErr := writer.ensureCommitted()
		if handlerErr != nil && commitErr != nil {
			return errors.Join(handlerErr, commitErr)
		}
		if handlerErr != nil {
			return handlerErr
		}
		return commitErr
	}
}

func (store *CookieStore) load(request *http.Request) (*Session, error) {
	cookie, err := request.Cookie(store.config.Name)
	now := store.now()
	if err == nil && len(cookie.Value) <= 3800 {
		parts := strings.Split(cookie.Value, ".")
		if len(parts) == 3 && parts[0] == "v1" {
			for i, key := range store.keys {
				if parts[1] != key.id || (!key.acceptUntil.IsZero() && !now.Before(key.acceptUntil)) {
					continue
				}
				encoded, decodeErr := base64.RawURLEncoding.Strict().DecodeString(parts[2])
				if decodeErr != nil || len(encoded) < key.aead.NonceSize()+key.aead.Overhead() {
					break
				}
				payload, openErr := key.aead.Open(nil, encoded[:key.aead.NonceSize()], encoded[key.aead.NonceSize():], store.aad(key.id))
				var record sessionRecord
				if openErr == nil && json.Unmarshal(payload, &record) == nil && record.valid(now, store.config) {
					return &Session{id: record.ID, values: record.Values, created: record.Created, touched: record.Touched, loaded: true, dirty: i > 0}, nil
				}
				break
			}
		}
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	return &Session{id: id, values: make(map[string]json.RawMessage), created: now}, nil
}

func (store *CookieStore) aad(keyID string) []byte {
	return []byte(store.config.Name + "\x00v1\x00" + keyID)
}

func (store *CookieStore) commit(writer http.ResponseWriter, current *Session) error {
	current.mu.Lock()
	defer current.mu.Unlock()
	current.committed = true
	dirty, destroyed := current.dirty, current.destroyed
	if destroyed {
		http.SetCookie(writer, cookieFromConfig(store.config, "", -1))
		return nil
	}
	if !dirty && !current.loaded {
		return nil
	}
	now := store.now()
	ttl := min(store.config.TTL, current.created.Add(store.config.AbsoluteTTL).Sub(now))
	if ttl <= 0 || (current.loaded && !now.Before(current.touched.Add(store.config.TTL))) {
		return web.NewHTTPError(http.StatusConflict, "session expired", ErrConflict)
	}
	key := store.keys[0]
	if !key.acceptUntil.IsZero() && !now.Before(key.acceptUntil) {
		return errors.New("session: active cookie key expired")
	}
	revision, err := randomID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sessionRecord{Version: 1, ID: current.id, Revision: revision, Created: current.created, Touched: now, Values: current.values})
	if err != nil {
		return fmt.Errorf("session: encode cookie: %w", err)
	}
	nonce := make([]byte, key.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("session: generate cookie nonce: %w", err)
	}
	sealed := key.aead.Seal(nil, nonce, payload, store.aad(key.id))
	value := "v1." + key.id + "." + base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(value) > 3800 {
		return errors.New("session: encrypted cookie exceeds 3800 bytes")
	}
	cookie := cookieFromConfig(store.config, value, cookieAge(ttl))
	if len(cookie.String()) > 4096 {
		return errors.New("session: serialized cookie exceeds 4096 bytes")
	}
	http.SetCookie(writer, cookie)
	current.dirty = false
	return nil
}
