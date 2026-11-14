package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func jwtFixture(t *testing.T) (*JWTVerifier, ed25519.PrivateKey, jwt.RegisteredClaims) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	v, err := NewJWTVerifier(JWTConfig{Issuer: "https://issuer.example", Audience: "api", Algorithms: []string{"EdDSA"}}, []JWTKey{{ID: "old", Algorithm: "EdDSA", PublicKey: public}})
	if err != nil {
		t.Fatal(err)
	}
	v.now = func() time.Time { return now }
	return v, private, jwt.RegisteredClaims{Issuer: "https://issuer.example", Subject: "alice", Audience: jwt.ClaimStrings{"api"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)), ID: "token-id"}
}

func signJWT(t *testing.T, method jwt.SigningMethod, key any, claims jwt.Claims, header map[string]any) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	token.Header["typ"], token.Header["kid"] = "at+jwt", "old"
	for name, value := range header {
		token.Header[name] = value
	}
	encoded, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestJWTRequiredClaimsAlgorithmsAndSignature(t *testing.T) {
	v, key, base := jwtFixture(t)
	valid := signJWT(t, jwt.SigningMethodEdDSA, key, base, nil)
	principal, err := v.Verify(t.Context(), valid)
	if err != nil || principal.Subject != "alice" || principal.Issuer != base.Issuer || principal.TokenID != base.ID {
		t.Fatal("valid token rejected", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*jwt.RegisteredClaims)
		header map[string]any
	}{
		{name: "issuer", mutate: func(c *jwt.RegisteredClaims) { c.Issuer = "other" }},
		{name: "missing issuer", mutate: func(c *jwt.RegisteredClaims) { c.Issuer = "" }},
		{name: "audience", mutate: func(c *jwt.RegisteredClaims) { c.Audience = jwt.ClaimStrings{"other"} }},
		{name: "missing audience", mutate: func(c *jwt.RegisteredClaims) { c.Audience = nil }},
		{name: "missing expiration", mutate: func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil }},
		{name: "expired", mutate: func(c *jwt.RegisteredClaims) { c.ExpiresAt = jwt.NewNumericDate(v.now()) }},
		{name: "missing issued at", mutate: func(c *jwt.RegisteredClaims) { c.IssuedAt = nil }},
		{name: "future issued at", mutate: func(c *jwt.RegisteredClaims) { c.IssuedAt = jwt.NewNumericDate(v.now().Add(time.Second)) }},
		{name: "future nbf", mutate: func(c *jwt.RegisteredClaims) { c.NotBefore = jwt.NewNumericDate(v.now().Add(time.Second)) }},
		{name: "excess lifetime", mutate: func(c *jwt.RegisteredClaims) { c.ExpiresAt = jwt.NewNumericDate(v.now().Add(time.Hour)) }},
		{name: "empty subject", mutate: func(c *jwt.RegisteredClaims) { c.Subject = " " }},
		{name: "long subject", mutate: func(c *jwt.RegisteredClaims) { c.Subject = strings.Repeat("x", 257) }},
		{name: "wrong typ", header: map[string]any{"typ": "JWT"}},
		{name: "unknown kid", header: map[string]any{"kid": "unknown"}},
		{name: "missing kid", header: map[string]any{"kid": nil}},
		{name: "remote key", header: map[string]any{"jku": "https://attacker.example/keys"}},
		{name: "embedded key", header: map[string]any{"jwk": map[string]any{"kty": "OKP"}}},
		{name: "critical extension", header: map[string]any{"crit": []string{"custom"}}},
		{name: "unencoded payload", header: map[string]any{"b64": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := base
			if tc.mutate != nil {
				tc.mutate(&claims)
			}
			encoded := signJWT(t, jwt.SigningMethodEdDSA, key, claims, tc.header)
			if _, err := v.Verify(t.Context(), encoded); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("invalid token accepted: %v", err)
			}
		})
	}
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{"", "a.b.c", valid + "x", strings.Repeat("a", 8193), signJWT(t, jwt.SigningMethodEdDSA, wrong, base, nil), signJWT(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, base, nil), signJWT(t, jwt.SigningMethodHS256, []byte(key.Public().(ed25519.PublicKey)), base, nil)} {
		if _, err := v.Verify(t.Context(), encoded); !errors.Is(err, ErrInvalidToken) {
			t.Fatal("signature/algorithm bypass", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := v.Verify(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestJWTKeyRotationAtomicSnapshotsAndGraceExpiry(t *testing.T) {
	v, old, claims := jwtFixture(t)
	public, next, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := JWTKey{ID: "old", Algorithm: "EdDSA", PublicKey: old.Public(), AcceptUntil: v.now().Add(time.Second)}
	newKey := JWTKey{ID: "new", Algorithm: "EdDSA", PublicKey: public}
	oldToken := signJWT(t, jwt.SigningMethodEdDSA, old, claims, nil)
	newToken := signJWT(t, jwt.SigningMethodEdDSA, next, claims, map[string]any{"kid": "new"})
	if err := v.ReplaceKeys([]JWTKey{newKey, oldKey}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{oldToken, newToken} {
		if _, err := v.Verify(t.Context(), token); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				if _, err := v.Verify(t.Context(), newToken); err != nil {
					t.Error("atomic rotation lost retained key", err)
					return
				}
			}
		})
	}
	for range 25 {
		if err := v.ReplaceKeys([]JWTKey{newKey}); err != nil {
			t.Fatal(err)
		}
		if err := v.ReplaceKeys([]JWTKey{newKey, oldKey}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	now := v.now().Add(time.Second)
	v.now = func() time.Time { return now }
	if _, err := v.Verify(t.Context(), oldToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("old key accepted past grace deadline")
	}
	if err := v.ReplaceKeys([]JWTKey{newKey}); err != nil {
		t.Fatal(err)
	}
	clear(public) // Constructor/update must clone caller-owned public keys.
	if _, err := v.Verify(t.Context(), newToken); err != nil {
		t.Fatal("caller mutated active key", err)
	}
	if err := v.ReplaceKeys([]JWTKey{{ID: "bad", Algorithm: "EdDSA", PublicKey: []byte("not a public key")}}); err == nil {
		t.Fatal("invalid key update accepted")
	}
	if _, err := v.Verify(t.Context(), newToken); err != nil {
		t.Fatal("failed update discarded old key set", err)
	}
	if err := v.ReplaceKeys(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), newToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("removed key remained trusted")
	}
}

func TestJWTRSAAndConfigurationValidation(t *testing.T) {
	_, signingKey, claims := jwtFixture(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	config := JWTConfig{Issuer: claims.Issuer, Audience: "api", Algorithms: []string{"RS256"}}
	v, err := NewJWTVerifier(config, []JWTKey{{ID: "old", Algorithm: "RS256", PublicKey: &key.PublicKey}})
	if err != nil {
		t.Fatal(err)
	}
	v.now = func() time.Time { return claims.IssuedAt.Time }
	if _, err := v.Verify(t.Context(), signJWT(t, jwt.SigningMethodRS256, key, claims, nil)); err != nil {
		t.Fatal(err)
	}
	for _, config := range []JWTConfig{{}, {Issuer: "i", Audience: "a", Algorithms: []string{"none"}}, {Issuer: "i", Audience: "a", Algorithms: []string{"HS256"}}, {Issuer: "i", Audience: "a", Algorithms: []string{"EdDSA", "EdDSA"}}, {Issuer: "i", Audience: "a", Algorithms: []string{"EdDSA"}, Leeway: 3 * time.Minute}, {Issuer: "i", Audience: "a", Algorithms: []string{"EdDSA"}, MaxLifetime: 25 * time.Hour}} {
		// Supply a key so each case reaches configuration validation instead of
		// always failing the unrelated empty-key-set check.
		if _, err := NewJWTVerifier(config, []JWTKey{{ID: "old", Algorithm: "EdDSA", PublicKey: signingKey.Public()}}); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	for _, keys := range [][]JWTKey{{{ID: "", Algorithm: "RS256", PublicKey: &key.PublicKey}}, {{ID: "k", Algorithm: "EdDSA", PublicKey: &key.PublicKey}}, {{ID: "k", Algorithm: "RS256", PublicKey: (*rsa.PublicKey)(nil)}}, {{ID: "k", Algorithm: "RS256", PublicKey: &key.PublicKey}, {ID: "k", Algorithm: "RS256", PublicKey: &key.PublicKey}}, make([]JWTKey, 17)} {
		if err := v.ReplaceKeys(keys); err == nil {
			t.Fatal("invalid keys accepted")
		}
	}
}
