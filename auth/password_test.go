package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func testHasher(t *testing.T) *PasswordHasher {
	t.Helper()
	h, err := NewPasswordHasher(PasswordConfig{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPasswordHashVerifyRehashAndBounds(t *testing.T) {
	low := testHasher(t)
	password := " unicode пароль with spaces "
	first, err := low.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := low.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, password) {
		t.Fatal("salt reuse or plaintext password")
	}
	if rehash, err := low.Verify(t.Context(), password, first); err != nil || rehash {
		t.Fatalf("verify: %v %v", rehash, err)
	}
	for _, wrong := range []string{"wrong", strings.TrimSpace(password), "", strings.Repeat("x", 1025)} {
		if _, err := low.Verify(t.Context(), wrong, first); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong password accepted: %v", err)
		}
	}
	high, err := NewPasswordHasher(PasswordConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if rehash, err := high.Verify(t.Context(), password, first); err != nil || !rehash {
		t.Fatal("old costs did not request rehash", err)
	}
	upgraded, err := high.Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if rehash, err := low.Verify(t.Context(), password, upgraded); err != nil || rehash {
		t.Fatal("stronger hash requested downgrade", err)
	}
	for _, encoded := range []string{"", strings.Repeat("x", 257), "$bcrypt$bad", strings.Replace(first, "v=19", "v=16", 1), strings.Replace(first, "m=8192", "m=4294967295", 1), strings.Replace(first, "t=1", "t=100", 1), strings.Replace(first, "p=1", "p=256", 1), strings.Replace(first, "p=1", "p=0", 1), strings.Replace(first, "m=8192", "m=08192", 1), first + "="} {
		if _, err := low.Verify(t.Context(), password, encoded); !errors.Is(err, ErrInvalidPasswordHash) {
			t.Fatalf("invalid PHC accepted: %q %v", encoded, err)
		}
	}
	for _, password := range []string{"", strings.Repeat("x", 1025)} {
		if _, err := low.Hash(t.Context(), password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	for _, config := range []PasswordConfig{{MemoryKiB: 1}, {MemoryKiB: 65537}, {Iterations: 6}, {Parallelism: 5}, {MaxConcurrent: -1}, {MaxConcurrent: 33}} {
		if _, err := NewPasswordHasher(config); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}

func TestPasswordHashCancellationWhileWaitingForCapacity(t *testing.T) {
	hasher := testHasher(t)
	hasher.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := hasher.Hash(ctx, "password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-hasher.slots
	if _, err := hasher.Hash(ctx, "password"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
