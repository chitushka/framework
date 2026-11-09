// Package cache provides a common cache API and interchangeable storage drivers.
package cache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// Backend is the minimal contract implemented by cache storage drivers.
// Implementations must copy byte slices they retain or return.
type Backend interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, string) error
}

// Dependency describes state that makes an item stale when its fingerprint changes.
type Dependency interface {
	Fingerprint(context.Context) (string, error)
}

// DependencyFunc adapts a function to Dependency.
type DependencyFunc func(context.Context) (string, error)

func (function DependencyFunc) Fingerprint(ctx context.Context) (string, error) {
	return function(ctx)
}

// Options controls item lifetime and invalidation dependencies.
type Options struct {
	TTL          time.Duration
	Tags         []string
	Dependencies []Dependency
}

// Cache is the storage-independent cache contract.
type Cache interface {
	Get(context.Context, string) ([]byte, bool, error)
	Set(context.Context, string, []byte, Options) error
	Delete(context.Context, string) error
	Clear(context.Context) error
	InvalidateTags(context.Context, ...string) error
}

// Store implements Cache on top of a Backend. Namespace and tag generations make
// invalidation independent of backend-specific enumeration commands.
type Store struct {
	backend Backend
	prefix  string
	now     func() time.Time
	mu      sync.RWMutex
	deps    map[string][]Dependency
	flights map[string]*loadFlight
}

type envelope struct {
	Value        []byte            `json:"value"`
	ExpiresAt    int64             `json:"expiresAt,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Dependencies []string          `json:"dependencies,omitempty"`
}

// New creates a cache store. Prefix separates multiple logical caches in one backend.
func New(backend Backend, prefix string) (*Store, error) {
	if isNilInterface(backend) {
		return nil, errors.New("cache: backend is required")
	}
	if strings.ContainsRune(prefix, '\x00') {
		return nil, errors.New("cache: prefix contains a NUL byte")
	}
	return &Store{backend: backend, prefix: prefix, now: time.Now, deps: make(map[string][]Dependency)}, nil
}

// Get returns an independent copy of a value. Expired or invalidated items are misses.
func (store *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if err := validateContextKey(ctx, key); err != nil {
		return nil, false, err
	}
	generation, err := store.generation(ctx, "namespace")
	if err != nil {
		return nil, false, err
	}
	physical := store.itemKey(generation, key)
	raw, found, err := store.backend.Get(ctx, physical)
	if err != nil || !found {
		return nil, found, err
	}
	var item envelope
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, false, fmt.Errorf("cache: decode item %q: %w", key, err)
	}
	if item.ExpiresAt > 0 && !store.now().Before(time.Unix(0, item.ExpiresAt)) {
		return nil, false, nil
	}
	for tag, expected := range item.Tags {
		actual, err := store.generation(ctx, "tag:"+tag)
		if err != nil {
			return nil, false, err
		}
		if actual != expected {
			return nil, false, nil
		}
	}
	dependencies := store.dependencies(physical)
	if len(item.Dependencies) > 0 && len(dependencies) != len(item.Dependencies) {
		// A dependency-bearing entry created by another process is conservatively
		// treated as stale because Go callbacks cannot be serialized.
		return nil, false, nil
	}
	for index, dependency := range dependencies {
		actual, err := dependency.Fingerprint(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("cache: check dependency %d: %w", index, err)
		}
		if actual != item.Dependencies[index] {
			return nil, false, nil
		}
	}
	return append([]byte(nil), item.Value...), true, nil
}

// Set stores a value and snapshots all tags and dependencies atomically from the
// caller's point of view. A negative TTL is rejected; zero means no expiration.
func (store *Store) Set(ctx context.Context, key string, value []byte, options Options) error {
	if err := validateContextKey(ctx, key); err != nil {
		return err
	}
	if options.TTL < 0 {
		return errors.New("cache: TTL cannot be negative")
	}
	tags, err := normalizeTags(options.Tags)
	if err != nil {
		return err
	}
	generation, err := store.generation(ctx, "namespace")
	if err != nil {
		return err
	}
	item := envelope{Value: append([]byte(nil), value...)}
	if options.TTL > 0 {
		item.ExpiresAt = store.now().Add(options.TTL).UnixNano()
	}
	if len(tags) > 0 {
		item.Tags = make(map[string]string, len(tags))
		for _, tag := range tags {
			version, err := store.generation(ctx, "tag:"+tag)
			if err != nil {
				return err
			}
			item.Tags[tag] = version
		}
	}
	for index, dependency := range options.Dependencies {
		if isNilInterface(dependency) {
			return fmt.Errorf("cache: dependency %d is nil", index)
		}
		fingerprint, err := dependency.Fingerprint(ctx)
		if err != nil {
			return fmt.Errorf("cache: snapshot dependency %d: %w", index, err)
		}
		item.Dependencies = append(item.Dependencies, fingerprint)
	}
	// Evaluators are process values and cannot be serialized. They are registered
	// under the physical key for subsequent reads by this Store instance.
	store.setDependencies(store.itemKey(generation, key), options.Dependencies)
	raw, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("cache: encode item %q: %w", key, err)
	}
	return store.backend.Set(ctx, store.itemKey(generation, key), raw, options.TTL)
}

// Delete removes a key from the current namespace generation.
func (store *Store) Delete(ctx context.Context, key string) error {
	if err := validateContextKey(ctx, key); err != nil {
		return err
	}
	generation, err := store.generation(ctx, "namespace")
	if err != nil {
		return err
	}
	physical := store.itemKey(generation, key)
	store.deleteDependencies(physical)
	return store.backend.Delete(ctx, physical)
}

// Clear invalidates all items in this logical cache in constant time.
func (store *Store) Clear(ctx context.Context) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	if err := store.rotate(ctx, "namespace"); err != nil {
		return err
	}
	store.mu.Lock()
	clear(store.deps)
	store.mu.Unlock()
	return nil
}

// InvalidateTags invalidates every item associated with at least one tag.
func (store *Store) InvalidateTags(ctx context.Context, tags ...string) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	normalized, err := normalizeTags(tags)
	if err != nil {
		return err
	}
	for _, tag := range normalized {
		if err := store.rotate(ctx, "tag:"+tag); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) generation(ctx context.Context, name string) (string, error) {
	value, found, err := store.backend.Get(ctx, store.metaKey(name))
	if err != nil {
		return "", fmt.Errorf("cache: read %s generation: %w", name, err)
	}
	if !found {
		return "", nil
	}
	return string(value), nil
}

func (store *Store) rotate(ctx context.Context, name string) error {
	version := make([]byte, 16)
	if _, err := rand.Read(version); err != nil {
		return fmt.Errorf("cache: create generation: %w", err)
	}
	if err := store.backend.Set(ctx, store.metaKey(name), []byte(hex.EncodeToString(version)), 0); err != nil {
		return fmt.Errorf("cache: rotate %s generation: %w", name, err)
	}
	return nil
}

func (store *Store) metaKey(name string) string { return digest(store.prefix + "\x00meta\x00" + name) }
func (store *Store) itemKey(generation, key string) string {
	return digest(store.prefix + "\x00item\x00" + generation + "\x00" + key)
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "chitu:" + hex.EncodeToString(sum[:])
}

func validateContextKey(ctx context.Context, key string) error {
	if ctx == nil {
		return errors.New("cache: context is nil")
	}
	if key == "" {
		return errors.New("cache: key is empty")
	}
	return nil
}

func normalizeTags(tags []string) ([]string, error) {
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return nil, errors.New("cache: tag is empty")
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	return result, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (store *Store) setDependencies(key string, dependencies []Dependency) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(dependencies) == 0 {
		delete(store.deps, key)
		return
	}
	if _, exists := store.deps[key]; !exists && len(store.deps) >= 10000 {
		for stale := range store.deps {
			delete(store.deps, stale)
			break
		}
	}
	store.deps[key] = append([]Dependency(nil), dependencies...)
}

func (store *Store) dependencies(key string) []Dependency {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return append([]Dependency(nil), store.deps[key]...)
}

func (store *Store) deleteDependencies(key string) {
	store.mu.Lock()
	delete(store.deps, key)
	store.mu.Unlock()
}
