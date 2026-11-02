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

	"github.com/chitushka/framework/web"
)

// CookieStore keeps the complete session in an authenticated, encrypted cookie.
// It is suitable for small sessions that do not require server-side invalidation.
type CookieStore struct {
	aead   cipher.AEAD
	config Config
}

// NewCookieStore creates stateless cookie storage. Secret must contain at least
// 32 random bytes and must remain stable across application instances and restarts.
func NewCookieStore(secret []byte, config Config) (*CookieStore, error) {
	if len(secret) < 32 {
		return nil, errors.New("session: cookie secret must contain at least 32 bytes")
	}
	validated, err := normalizedConfig(config)
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("session: create cookie cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("session: create cookie AEAD: %w", err)
	}
	return &CookieStore{aead: aead, config: validated}, nil
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
		ctx.Writer = writer
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
	values := make(map[string]json.RawMessage)
	cookie, err := request.Cookie(store.config.Name)
	if err == nil && cookie.Value != "" {
		encoded, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value)
		if decodeErr == nil && len(encoded) >= store.aead.NonceSize() {
			nonce, ciphertext := encoded[:store.aead.NonceSize()], encoded[store.aead.NonceSize():]
			payload, openErr := store.aead.Open(nil, nonce, ciphertext, []byte(store.config.Name))
			if openErr == nil {
				_ = json.Unmarshal(payload, &values)
			}
		}
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	return &Session{id: id, values: values}, nil
}

func (store *CookieStore) commit(writer http.ResponseWriter, current *Session) error {
	current.mu.RLock()
	dirty, destroyed := current.dirty, current.destroyed
	values := make(map[string]json.RawMessage, len(current.values))
	for key, value := range current.values {
		values[key] = append([]byte(nil), value...)
	}
	current.mu.RUnlock()
	if destroyed {
		http.SetCookie(writer, cookieFromConfig(store.config, "", -1))
		return nil
	}
	if !dirty {
		return nil
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("session: encode cookie: %w", err)
	}
	nonce := make([]byte, store.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("session: generate cookie nonce: %w", err)
	}
	sealed := store.aead.Seal(nil, nonce, payload, []byte(store.config.Name))
	value := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(value) > 3800 {
		return errors.New("session: encrypted cookie exceeds 3800 bytes")
	}
	http.SetCookie(writer, cookieFromConfig(store.config, value, int(store.config.TTL.Seconds())))
	current.mu.Lock()
	current.dirty = false
	current.mu.Unlock()
	return nil
}
