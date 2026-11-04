package auth

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"errors"
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type JWTConfig struct {
	Issuer   string
	Audience string
	// Algorithms must explicitly list EdDSA and/or RS256. HMAC/none are unsupported.
	Algorithms []string
	// TokenType defaults to at+jwt; configure JWT explicitly for providers using it.
	TokenType   string
	MaxLifetime time.Duration // default 15 minutes; maximum 24 hours
	Leeway      time.Duration // maximum 2 minutes; default zero
}

// JWTKey binds an operator-supplied kid to one algorithm and public key.
// AcceptUntil optionally bounds a previous key's rotation grace period.
type JWTKey struct {
	ID          string
	Algorithm   string
	PublicKey   crypto.PublicKey
	AcceptUntil time.Time
}

type jwtKeySet struct{ keys map[string]JWTKey }

// JWTVerifier uses an immutable, atomically replaced trusted public-key set.
// It never fetches token-supplied URLs or accepts token-supplied public keys.
type JWTVerifier struct {
	config JWTConfig
	keys   atomic.Pointer[jwtKeySet]
	now    func() time.Time
}

func NewJWTVerifier(config JWTConfig, keys []JWTKey) (*JWTVerifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("auth: initial JWT key set cannot be empty")
	}
	if config.Issuer == "" || config.Audience == "" || len(config.Issuer) > 1024 || len(config.Audience) > 1024 || len(config.Algorithms) == 0 || len(config.Algorithms) > 2 {
		return nil, errors.New("auth: JWT issuer, audience and algorithms are required")
	}
	config.Algorithms = append([]string(nil), config.Algorithms...)
	for i, algorithm := range config.Algorithms {
		if (algorithm != "EdDSA" && algorithm != "RS256") || (i > 0 && algorithm == config.Algorithms[0]) {
			return nil, errors.New("auth: unsupported or duplicate JWT algorithm")
		}
	}
	if config.TokenType == "" {
		config.TokenType = "at+jwt"
	}
	if config.MaxLifetime == 0 {
		config.MaxLifetime = 15 * time.Minute
	}
	if config.MaxLifetime < time.Second || config.MaxLifetime > 24*time.Hour || config.Leeway < 0 || config.Leeway > 2*time.Minute || len(config.TokenType) > 64 {
		return nil, errors.New("auth: invalid JWT lifetime, leeway or token type")
	}
	verifier := &JWTVerifier{config: config, now: time.Now}
	if err := verifier.ReplaceKeys(keys); err != nil {
		return nil, err
	}
	return verifier, nil
}

// ReplaceKeys validates a complete snapshot before swapping it. Invalid updates
// preserve the old set. Empty sets deliberately revoke all keys. In-flight calls
// may finish with the snapshot they loaded before replacement.
func (v *JWTVerifier) ReplaceKeys(keys []JWTKey) error {
	if len(keys) > 16 {
		return errors.New("auth: JWT key set exceeds 16 keys")
	}
	snapshot := &jwtKeySet{keys: make(map[string]JWTKey, len(keys))}
	for _, entry := range keys {
		allowed := false
		for _, algorithm := range v.config.Algorithms {
			allowed = allowed || entry.Algorithm == algorithm
		}
		if !allowed || entry.ID == "" || len(entry.ID) > 128 || strings.TrimSpace(entry.ID) != entry.ID || strings.ContainsAny(entry.ID, "\r\n\x00") {
			return errors.New("auth: invalid JWT key ID or algorithm")
		}
		if _, exists := snapshot.keys[entry.ID]; exists {
			return errors.New("auth: duplicate JWT key ID")
		}
		switch key := entry.PublicKey.(type) {
		case ed25519.PublicKey:
			if entry.Algorithm != "EdDSA" || len(key) != ed25519.PublicKeySize {
				return errors.New("auth: invalid EdDSA public key")
			}
			entry.PublicKey = append(ed25519.PublicKey(nil), key...)
		case *rsa.PublicKey:
			if entry.Algorithm != "RS256" || key == nil || key.N == nil || key.N.Sign() <= 0 || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.E < 3 || key.E%2 == 0 {
				return errors.New("auth: invalid RSA public key")
			}
			entry.PublicKey = &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
		default:
			return errors.New("auth: unsupported JWT public key type")
		}
		snapshot.keys[entry.ID] = entry
	}
	v.keys.Store(snapshot)
	return nil
}

func (v *JWTVerifier) Verify(ctx context.Context, encoded string) (Principal, error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if encoded == "" || len(encoded) > 8192 {
		return Principal{}, ErrInvalidToken
	}
	keys := v.keys.Load()
	now := v.now()
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(encoded, claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok || len(kid) > 128 {
			return nil, ErrInvalidToken
		}
		key, exists := keys.keys[kid]
		if !exists || token.Method.Alg() != key.Algorithm || (!key.AcceptUntil.IsZero() && !now.Before(key.AcceptUntil)) {
			return nil, ErrInvalidToken
		}
		if token.Header["typ"] != v.config.TokenType {
			return nil, ErrInvalidToken
		}
		for _, name := range []string{"crit", "jku", "jwk", "x5u", "b64"} {
			if _, exists := token.Header[name]; exists {
				return nil, ErrInvalidToken
			}
		}
		return key.PublicKey, nil
	}, jwt.WithValidMethods(v.config.Algorithms), jwt.WithIssuer(v.config.Issuer), jwt.WithAudience(v.config.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(v.config.Leeway), jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithStrictDecoding())
	if err != nil || token == nil || !token.Valid || claims.IssuedAt == nil || claims.ExpiresAt == nil ||
		strings.TrimSpace(claims.Subject) == "" || len(claims.Subject) > 256 || len(claims.ID) > 256 ||
		!claims.ExpiresAt.After(claims.IssuedAt.Time) || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > v.config.MaxLifetime {
		return Principal{}, ErrInvalidToken
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	return Principal{Subject: claims.Subject, Issuer: claims.Issuer, Audience: append([]string(nil), claims.Audience...), IssuedAt: claims.IssuedAt.Time, ExpiresAt: claims.ExpiresAt.Time, TokenID: claims.ID}, nil
}
