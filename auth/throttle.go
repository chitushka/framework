package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"
)

var (
	ErrThrottled       = errors.New("auth: login attempts exceeded")
	ErrLimiterCapacity = errors.New("auth: login limiter capacity exhausted")
)

type ThrottleError struct{ RetryAfter time.Duration }

func (e *ThrottleError) Error() string { return ErrThrottled.Error() }
func (e *ThrottleError) Unwrap() error { return ErrThrottled }

// AttemptLimiter atomically reserves one attempt for BOTH canonical account and
// trusted client keys. Positive retryAfter means denied, with no counter changes.
// External implementations must share counters across all application instances.
type AttemptLimiter interface {
	Allow(ctx context.Context, account, client string) (retryAfter time.Duration, err error)
}

type AttemptLimitConfig struct {
	AccountLimit int
	ClientLimit  int
	Window       time.Duration
	MaxEntries   int
}

type attemptWindow struct {
	count int
	until time.Time
}

// MemoryAttemptLimiter is bounded, fail-closed, process-local fixed-window
// throttling. Every attempt counts, including success; there is no reset bypass.
type MemoryAttemptLimiter struct {
	mu      sync.Mutex
	config  AttemptLimitConfig
	entries map[[32]byte]attemptWindow
	now     func() time.Time
}

func NewMemoryAttemptLimiter(config AttemptLimitConfig) (*MemoryAttemptLimiter, error) {
	if config.AccountLimit == 0 {
		config.AccountLimit = 5
	}
	if config.ClientLimit == 0 {
		config.ClientLimit = 30
	}
	if config.Window == 0 {
		config.Window = time.Minute
	}
	if config.MaxEntries == 0 {
		config.MaxEntries = 10000
	}
	if config.AccountLimit < 1 || config.ClientLimit < 1 || config.Window < time.Second || config.Window > 24*time.Hour || config.MaxEntries < 2 || config.MaxEntries > 100000 {
		return nil, errors.New("auth: invalid login limits")
	}
	return &MemoryAttemptLimiter{config: config, entries: make(map[[32]byte]attemptWindow), now: time.Now}, nil
}

func (m *MemoryAttemptLimiter) Allow(ctx context.Context, account, client string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if account == "" || client == "" || len(account) > 256 || len(client) > 256 {
		return 0, errors.New("auth: invalid login limiter keys")
	}
	keys := [2][32]byte{sha256.Sum256([]byte("account:" + account)), sha256.Sum256([]byte("client:" + client))}
	limits := [2]int{m.config.AccountLimit, m.config.ClientLimit}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := m.now()
	var retry time.Duration
	missing := 0
	for i, key := range keys {
		entry, exists := m.entries[key]
		if exists && !now.Before(entry.until) {
			delete(m.entries, key)
			exists = false
		}
		if !exists {
			missing++
			continue
		}
		if entry.count >= limits[i] {
			retry = max(retry, entry.until.Sub(now))
		}
	}
	if retry > 0 {
		return retry, nil
	}
	if len(m.entries)+missing > m.config.MaxEntries {
		for key, entry := range m.entries {
			if !now.Before(entry.until) {
				delete(m.entries, key)
			}
		}
		if len(m.entries)+missing > m.config.MaxEntries {
			return 0, ErrLimiterCapacity
		}
	}
	for _, key := range keys {
		entry, exists := m.entries[key]
		if !exists {
			entry.until = now.Add(m.config.Window)
		}
		entry.count++
		m.entries[key] = entry
	}
	return 0, nil
}
