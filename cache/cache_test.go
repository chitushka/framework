package cache

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type failingBackend struct{ err error }

func (backend failingBackend) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, backend.err
}
func (backend failingBackend) Set(context.Context, string, []byte, time.Duration) error {
	return backend.err
}
func (backend failingBackend) Delete(context.Context, string) error { return backend.err }

func TestStoreLifecycleTTLTagsAndClear(t *testing.T) {
	ctx := context.Background()
	backend := NewMemoryBackend()
	now := time.Unix(100, 0)
	backend.now = func() time.Time { return now }
	store, err := New(backend, "tests")
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }

	if err := store.Set(ctx, "profile:1", []byte("first"), Options{TTL: time.Minute, Tags: []string{"users", " users "}}); err != nil {
		t.Fatal(err)
	}
	value, found, err := store.Get(ctx, "profile:1")
	if err != nil || !found || string(value) != "first" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
	value[0] = 'X'
	again, _, _ := store.Get(ctx, "profile:1")
	if string(again) != "first" {
		t.Fatal("returned value aliases stored bytes")
	}
	if err := store.InvalidateTags(ctx, "users"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, "profile:1"); err != nil || found {
		t.Fatalf("tag-invalidated item found=%v err=%v", found, err)
	}

	if err := store.Set(ctx, "a", []byte("A"), Options{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, "a"); found {
		t.Fatal("cleared item is still visible")
	}

	if err := store.Set(ctx, "short", []byte("value"), Options{TTL: time.Second}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, found, _ := store.Get(ctx, "short"); found {
		t.Fatal("expired item is still visible")
	}
}

func TestStoreTreatsForeignDependenciesAsStale(t *testing.T) {
	backend := NewMemoryBackend()
	writer, _ := New(backend, "shared")
	reader, _ := New(backend, "shared")
	if err := writer.Set(context.Background(), "key", []byte("value"), Options{
		Dependencies: []Dependency{DependencyFunc(func(context.Context) (string, error) { return "v1", nil })},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := reader.Get(context.Background(), "key"); err != nil || found {
		t.Fatalf("foreign dependency found=%v err=%v", found, err)
	}
}

func TestStoreRejectsCorruptEnvelope(t *testing.T) {
	backend := NewMemoryBackend()
	store, _ := New(backend, "")
	physical := store.itemKey("", "key")
	if err := backend.Set(context.Background(), physical, []byte("not-json"), 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "key"); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("Get error = %v", err)
	}
}

func TestStorePropagatesBackendAndDependencyReadErrors(t *testing.T) {
	want := errors.New("backend unavailable")
	store, _ := New(failingBackend{err: want}, "")
	ctx := context.Background()
	if _, _, err := store.Get(ctx, "key"); !errors.Is(err, want) {
		t.Fatalf("Get error = %v", err)
	}
	if err := store.Set(ctx, "key", nil, Options{}); !errors.Is(err, want) {
		t.Fatalf("Set error = %v", err)
	}
	if err := store.Delete(ctx, "key"); !errors.Is(err, want) {
		t.Fatalf("Delete error = %v", err)
	}
	if err := store.Clear(ctx); !errors.Is(err, want) {
		t.Fatalf("Clear error = %v", err)
	}

	memory := NewMemoryBackend()
	store, _ = New(memory, "")
	dependencyErr := errors.New("check failed")
	dependency := DependencyFunc(func(context.Context) (string, error) { return "initial", nil })
	if err := store.Set(ctx, "key", []byte("value"), Options{Dependencies: []Dependency{dependency}}); err != nil {
		t.Fatal(err)
	}
	store.setDependencies(store.itemKey("", "key"), []Dependency{DependencyFunc(func(context.Context) (string, error) {
		return "", dependencyErr
	})})
	if _, _, err := store.Get(ctx, "key"); !errors.Is(err, dependencyErr) {
		t.Fatalf("dependency Get error = %v", err)
	}
}

func TestStoreDependenciesAndDelete(t *testing.T) {
	ctx := context.Background()
	store, _ := New(NewMemoryBackend(), "")
	state := "one"
	dependency := DependencyFunc(func(context.Context) (string, error) { return state, nil })
	if err := store.Set(ctx, "key", []byte("value"), Options{Dependencies: []Dependency{dependency}}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(ctx, "key"); err != nil || !found {
		t.Fatalf("unchanged dependency found=%v err=%v", found, err)
	}
	state = "two"
	if _, found, err := store.Get(ctx, "key"); err != nil || found {
		t.Fatalf("changed dependency found=%v err=%v", found, err)
	}
	if err := store.Set(ctx, "key", []byte("value"), Options{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.Get(ctx, "key"); found {
		t.Fatal("deleted item is still visible")
	}
}

func TestStoreValidationAndDependencyErrors(t *testing.T) {
	if _, err := New(nil, ""); err == nil {
		t.Fatal("New(nil) succeeded")
	}
	var typedNil *MemoryBackend
	if _, err := New(typedNil, ""); err == nil {
		t.Fatal("New(typed nil) succeeded")
	}
	if _, err := New(NewMemoryBackend(), "bad\x00prefix"); err == nil {
		t.Fatal("NUL prefix accepted")
	}
	store, _ := New(NewMemoryBackend(), "")
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"empty key":    func() error { return store.Set(ctx, "", nil, Options{}) },
		"negative TTL": func() error { return store.Set(ctx, "x", nil, Options{TTL: -1}) },
		"empty tag":    func() error { return store.Set(ctx, "x", nil, Options{Tags: []string{" "}}) },
		"nil dep":      func() error { return store.Set(ctx, "x", nil, Options{Dependencies: []Dependency{nil}}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	want := errors.New("dependency unavailable")
	dependency := DependencyFunc(func(context.Context) (string, error) { return "", want })
	if err := store.Set(ctx, "x", nil, Options{Dependencies: []Dependency{dependency}}); !errors.Is(err, want) {
		t.Fatalf("Set error = %v", err)
	}
}

func TestMemoryBackendCopiesAndCancellation(t *testing.T) {
	backend := NewMemoryBackend()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.Set(ctx, "key", nil, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Set error = %v", err)
	}
	ctx = context.Background()
	input := []byte("value")
	if err := backend.Set(ctx, "key", input, 0); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	value, found, err := backend.Get(ctx, "key")
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("Get() = %q, %v, %v", value, found, err)
	}
}
