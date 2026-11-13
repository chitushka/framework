package main

import (
	"crypto/sha256"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/web"
)

type rateEntry struct {
	until time.Time
	count int
}
type rateLimiter struct {
	mu              sync.Mutex
	entries         map[[32]byte]rateEntry
	limit, capacity int
	now             func() time.Time
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{entries: make(map[[32]byte]rateEntry), limit: limit, capacity: 4096, now: time.Now}
}

// Fixed one-minute windows. Full tables fail closed without evicting live limits.
// Keys are fixed-size hashes; no background goroutine is needed.
func (l *rateLimiter) allow(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	hash := sha256.Sum256([]byte(key))
	entry, found := l.entries[hash]
	if !found && len(l.entries) >= l.capacity {
		for key, value := range l.entries {
			if !now.Before(value.until) {
				delete(l.entries, key)
			}
		}
		if len(l.entries) >= l.capacity {
			return 60
		}
	}
	if !now.Before(entry.until) {
		entry = rateEntry{until: now.Add(time.Minute)}
	}
	if entry.count >= l.limit {
		return max(1, int(entry.until.Sub(now).Seconds()+0.999))
	}
	entry.count++
	l.entries[hash] = entry
	return 0
}

func (l *rateLimiter) clients(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retry := l.allow((&web.Context{Request: r}).ClientIP()); retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *rateLimiter) subjects(next web.Handler) web.Handler {
	return func(ctx *web.Context) error {
		principal, ok := auth.PrincipalFromContext(ctx.Request.Context())
		if !ok {
			return web.NewHTTPError(503, "", nil)
		}
		if retry := l.allow(principal.Subject); retry > 0 {
			ctx.Writer.Header().Set("Retry-After", strconv.Itoa(retry))
			return web.NewHTTPError(429, "", nil)
		}
		return next(ctx)
	}
}
