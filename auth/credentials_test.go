package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type passwordIdentity string

func (id passwordIdentity) ID() string { return string(id) }

type credentialFixture struct {
	mu                 sync.Mutex
	credential         *Credential
	findErr, updateErr error
	conflict           bool
	lookups, updates   int
}

func (s *credentialFixture) FindCredential(_ context.Context, login string) (*Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	if s.findErr != nil {
		return nil, s.findErr
	}
	if login != "alice" || s.credential == nil {
		return nil, nil
	}
	copy := *s.credential
	return &copy, nil
}

func (s *credentialFixture) ReplacePasswordHash(_ context.Context, id, old, next string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updates++
	if s.updateErr != nil {
		return false, s.updateErr
	}
	if s.conflict || s.credential == nil || s.credential.Identity.ID() != id || s.credential.PasswordHash != old {
		return false, nil
	}
	s.credential.PasswordHash = next
	return true, nil
}

type fixtureLimiter struct {
	retry time.Duration
	err   error
}

func (l fixtureLimiter) Allow(context.Context, string, string) (time.Duration, error) {
	return l.retry, l.err
}

func TestCredentialVerificationNegativeCasesAndRehashCAS(t *testing.T) {
	low := testHasher(t)
	encoded, err := low.Hash(t.Context(), "correct password")
	if err != nil {
		t.Fatal(err)
	}
	high, err := NewPasswordHasher(PasswordConfig{MemoryKiB: 8 * 1024, Iterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	store := &credentialFixture{credential: &Credential{Identity: passwordIdentity("user-1"), PasswordHash: encoded}}
	service, err := NewPasswordAuthenticator(store, high, fixtureLimiter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ login, password string }{{"missing", "correct password"}, {"alice", "incorrect"}, {"alice", ""}, {"", "correct password"}, {"alice", strings.Repeat("x", 1025)}} {
		if _, err := service.Authenticate(t.Context(), tc.login, tc.password, "client"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal("invalid credentials accepted", err)
		}
	}
	identity, err := service.Authenticate(t.Context(), "alice", "correct password", "client")
	if err != nil || identity.ID() != "user-1" || store.updates != 1 || store.credential.PasswordHash == encoded {
		t.Fatal("authentication/rehash failed", err)
	}
	if _, err := service.Authenticate(t.Context(), "alice", "correct password", "client"); err != nil || store.updates != 1 {
		t.Fatal("rehash repeated", err)
	}
	store.credential.Disabled = true
	if _, err := service.Authenticate(t.Context(), "alice", "correct password", "client"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("disabled account accepted", err)
	}
	store.credential.Disabled = false
	store.credential.PasswordHash = "malformed"
	if _, err := service.Authenticate(t.Context(), "alice", "correct password", "client"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("corrupt hash accepted", err)
	}
	store.credential.PasswordHash = encoded
	store.conflict = true
	if _, err := service.Authenticate(t.Context(), "alice", "correct password", "client"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("concurrent password reset overwritten", err)
	}
}

func TestCredentialDependenciesFailClosedAndThrottleBeforeLookup(t *testing.T) {
	store := &credentialFixture{}
	want := errors.New("private storage failure")
	hasher := testHasher(t)
	service, err := NewPasswordAuthenticator(store, hasher, fixtureLimiter{retry: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(t.Context(), "alice", "password", "client"); !errors.Is(err, ErrThrottled) {
		t.Fatal(err)
	}
	if store.lookups != 0 {
		t.Fatal("throttle did not precede credential lookup")
	}
	service.limiter = fixtureLimiter{err: want}
	if _, err := service.Authenticate(t.Context(), "alice", "password", "client"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	service.limiter = fixtureLimiter{}
	store.findErr = want
	if _, err := service.Authenticate(t.Context(), "alice", "password", "client"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	store.findErr = nil
	encoded, err := hasher.Hash(t.Context(), "password")
	if err != nil {
		t.Fatal(err)
	}
	store.credential = &Credential{Identity: passwordIdentity("user-1"), PasswordHash: encoded}
	service.hasher, err = NewPasswordHasher(PasswordConfig{MemoryKiB: 8 * 1024, Iterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	store.updateErr = want
	if _, err := service.Authenticate(t.Context(), "alice", "password", "client"); !errors.Is(err, want) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.Authenticate(ctx, "alice", "password", "client"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, args := range []struct {
		store   CredentialStore
		hasher  *PasswordHasher
		limiter AttemptLimiter
	}{{nil, hasher, fixtureLimiter{}}, {store, nil, fixtureLimiter{}}, {store, hasher, nil}} {
		if _, err := NewPasswordAuthenticator(args.store, args.hasher, args.limiter); err == nil {
			t.Fatal("missing dependency accepted")
		}
	}
}

func TestLoginThrottleConcurrentLimitsCapacityAndExpiry(t *testing.T) {
	limiter, err := NewMemoryAttemptLimiter(AttemptLimitConfig{AccountLimit: 5, ClientLimit: 30, MaxEntries: 3})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	limiter.now = func() time.Time { return now }
	var wg sync.WaitGroup
	results := make(chan bool, 50)
	for range 50 {
		wg.Go(func() {
			retry, err := limiter.Allow(t.Context(), "alice", "client")
			results <- err == nil && retry == 0
		})
	}
	wg.Wait()
	close(results)
	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("concurrent limit bypass: %d", allowed)
	}
	if retry, err := limiter.Allow(t.Context(), "bob", "other"); !errors.Is(err, ErrLimiterCapacity) || retry != 0 {
		t.Fatal("capacity did not fail closed", retry, err)
	}
	if len(limiter.entries) > 3 {
		t.Fatal("limiter memory grew past bound")
	}
	now = now.Add(time.Minute)
	if retry, err := limiter.Allow(t.Context(), "bob", "other"); err != nil || retry != 0 {
		t.Fatal("expired capacity not reclaimed", err)
	}
	clientLimiter, _ := NewMemoryAttemptLimiter(AttemptLimitConfig{AccountLimit: 10, ClientLimit: 1})
	if _, err := clientLimiter.Allow(t.Context(), "alice", "client"); err != nil {
		t.Fatal(err)
	}
	if retry, err := clientLimiter.Allow(t.Context(), "bob", "client"); err != nil || retry <= 0 {
		t.Fatal("client limit bypass", retry, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := clientLimiter.Allow(ctx, "a", "b"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := clientLimiter.Allow(t.Context(), "a", ""); err == nil {
		t.Fatal("missing client key accepted")
	}
	for _, config := range []AttemptLimitConfig{{AccountLimit: -1}, {ClientLimit: -1}, {Window: time.Nanosecond}, {Window: 25 * time.Hour}, {MaxEntries: 1}} {
		if _, err := NewMemoryAttemptLimiter(config); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
}
