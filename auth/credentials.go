package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// CredentialStore owns canonical logins, disabled state and identity persistence.
// Missing accounts return (nil,nil). ReplacePasswordHash must compare the old
// hash atomically so a rehash cannot overwrite a concurrent password reset.
type CredentialStore interface {
	FindCredential(context.Context, string) (*Credential, error)
	ReplacePasswordHash(ctx context.Context, identityID, oldHash, newHash string) (bool, error)
}

type Credential struct {
	Identity     Identity
	PasswordHash string
	Disabled     bool
}

// PasswordAuthenticator verifies credentials but does not issue sessions/tokens.
// Call User.Login after success in a CSRF-protected browser login handler.
type PasswordAuthenticator struct {
	store   CredentialStore
	hasher  *PasswordHasher
	limiter AttemptLimiter
	dummy   string
}

func NewPasswordAuthenticator(store CredentialStore, hasher *PasswordHasher, limiter AttemptLimiter) (*PasswordAuthenticator, error) {
	if store == nil || hasher == nil || limiter == nil {
		return nil, errors.New("auth: credential store, password hasher and limiter are required")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	defer clear(secret)
	dummy, err := hasher.Hash(context.Background(), base64.RawStdEncoding.EncodeToString(secret))
	if err != nil {
		return nil, err
	}
	return &PasswordAuthenticator{store: store, hasher: hasher, limiter: limiter, dummy: dummy}, nil
}

// Authenticate requires a canonical login and a trusted, nonempty client key
// (for example proxy-policy-derived IP). No password trimming or truncation occurs.
// Unknown/disabled accounts and wrong passwords share ErrInvalidCredentials.
func (a *PasswordAuthenticator) Authenticate(ctx context.Context, login, password, client string) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if login == "" || len(login) > 256 || password == "" || len(password) > 1024 {
		return nil, ErrInvalidCredentials
	}
	if client == "" || len(client) > 256 {
		return nil, errors.New("auth: trusted client key is required")
	}
	retry, err := a.limiter.Allow(ctx, login, client)
	if err != nil {
		return nil, fmt.Errorf("auth: login limiter: %w", err)
	}
	if retry > 0 {
		return nil, &ThrottleError{RetryAfter: retry}
	}
	record, err := a.store.FindCredential(ctx, login)
	if err != nil {
		return nil, fmt.Errorf("auth: credentials: %w", err)
	}
	encoded := a.dummy
	known := record != nil && record.Identity != nil && record.Identity.ID() != "" && !record.Disabled
	if known {
		encoded = record.PasswordHash
	}
	needsRehash, err := a.hasher.Verify(ctx, password, encoded)
	if errors.Is(err, ErrInvalidPasswordHash) {
		// Corrupt/unsupported stored hashes must not bypass hashing work.
		_, dummyErr := a.hasher.Verify(ctx, password, a.dummy)
		if dummyErr != nil && !errors.Is(dummyErr, ErrInvalidCredentials) {
			return nil, dummyErr
		}
		return nil, ErrInvalidCredentials
	}
	if err != nil && !errors.Is(err, ErrInvalidCredentials) {
		return nil, err
	}
	if !known || err != nil {
		return nil, ErrInvalidCredentials
	}
	if needsRehash {
		replacement, err := a.hasher.Hash(ctx, password)
		if err != nil {
			return nil, err
		}
		replaced, err := a.store.ReplacePasswordHash(ctx, record.Identity.ID(), encoded, replacement)
		if err != nil {
			return nil, fmt.Errorf("auth: password rehash: %w", err)
		}
		if !replaced {
			return nil, ErrInvalidCredentials
		}
	}
	return record.Identity, nil
}
