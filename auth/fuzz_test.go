package auth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func FuzzJWT(f *testing.F) {
	// Deterministic test-only key lets every fuzz subprocess verify the corpus.
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	now := time.Unix(1800000000, 0)
	verifier, err := NewJWTVerifier(JWTConfig{Issuer: "https://issuer.example", Audience: "api", Algorithms: []string{"EdDSA"}}, []JWTKey{{ID: "test", Algorithm: "EdDSA", PublicKey: key.Public()}})
	if err != nil {
		f.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{Issuer: "https://issuer.example", Subject: "alice", Audience: jwt.ClaimStrings{"api"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute))})
	token.Header["typ"], token.Header["kid"] = "at+jwt", "test"
	valid, err := token.SignedString(key)
	if err != nil {
		f.Fatal(err)
	}
	for _, encoded := range []string{valid, valid + "x", "", "a.b.c", "eyJhbGciOiJub25lIn0.e30.", "...."} {
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		if len(encoded) > 16384 {
			t.Skip()
		}
		principal, err := verifier.Verify(t.Context(), encoded)
		if err != nil {
			if !errors.Is(err, ErrInvalidToken) || principal.Subject != "" {
				t.Fatalf("invalid token exposed an identity or unexpected error: %v", err)
			}
			return
		}
		if principal.Subject == "" || principal.Issuer != "https://issuer.example" || !principal.ExpiresAt.After(now) || principal.IssuedAt.After(now) || principal.ExpiresAt.Sub(principal.IssuedAt) > 15*time.Minute {
			t.Fatal("accepted token bypassed identity/lifetime policy")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := verifier.Verify(ctx, encoded); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled verification succeeded")
		}
	})
}

func FuzzPasswordHash(f *testing.F) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	for _, encoded := range []string{"", "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + key, "$argon2id$v=19$m=4294967295,t=1,p=256$" + salt + "$" + key} {
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, encoded string) {
		// Parse only: hostile PHC costs must never trigger an Argon2 allocation.
		cost, salt, key, err := parsePasswordHash(encoded)
		if err != nil {
			if !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatal(err)
			}
			return
		}
		if cost.MemoryKiB < 8192 || cost.MemoryKiB > 65536 || cost.Iterations < 1 || cost.Iterations > 5 || cost.Parallelism < 1 || cost.Parallelism > 4 || len(salt) != 16 || len(key) != 32 {
			t.Fatal("PHC parser accepted unbounded costs or invalid key lengths")
		}
		canonical := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", cost.MemoryKiB, cost.Iterations, cost.Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
		if !bytes.Equal([]byte(encoded), []byte(canonical)) {
			t.Fatal("PHC parser accepted a noncanonical encoding")
		}
	})
}
