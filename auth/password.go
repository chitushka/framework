package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidCredentials  = errors.New("auth: invalid credentials")
	ErrInvalidPasswordHash = errors.New("auth: unsupported or invalid password hash")
)

// PasswordConfig specifies Argon2id costs. Zero fields select 19 MiB, two
// iterations, one lane and two concurrent operations. Input is never truncated.
type PasswordConfig struct {
	MemoryKiB     uint32
	Iterations    uint32
	Parallelism   uint8
	MaxConcurrent int
}

// PasswordHasher uses fixed 16-byte salts and 32-byte keys with PHC encoding.
// Verification accepts bounded costs only: 8–64 MiB, 1–5 iterations, 1–4 lanes.
// Cancellation stops waiting for a slot; an executing Argon2 call finishes first.
type PasswordHasher struct {
	config PasswordConfig
	slots  chan struct{}
}

func NewPasswordHasher(config PasswordConfig) (*PasswordHasher, error) {
	if config.MemoryKiB == 0 {
		config.MemoryKiB = 19 * 1024
	}
	if config.Iterations == 0 {
		config.Iterations = 2
	}
	if config.Parallelism == 0 {
		config.Parallelism = 1
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 2
	}
	if !validPasswordCost(config) || config.MaxConcurrent < 1 || config.MaxConcurrent > 32 {
		return nil, errors.New("auth: invalid password hashing costs or concurrency")
	}
	return &PasswordHasher{config: config, slots: make(chan struct{}, config.MaxConcurrent)}, nil
}

func validPasswordCost(config PasswordConfig) bool {
	return config.MemoryKiB >= 8*1024 && config.MemoryKiB <= 64*1024 && config.Iterations >= 1 && config.Iterations <= 5 && config.Parallelism >= 1 && config.Parallelism <= 4
}

func (h *PasswordHasher) derive(ctx context.Context, password string, salt []byte, cost PasswordConfig) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if password == "" || len(password) > 1024 {
		return nil, ErrInvalidCredentials
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := argon2.IDKey([]byte(password), salt, cost.Iterations, cost.MemoryKiB, cost.Parallelism, 32)
	if err := ctx.Err(); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

func (h *PasswordHasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key, err := h.derive(ctx, password, salt, h.config)
	if err != nil {
		return "", err
	}
	defer clear(key)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", h.config.MemoryKiB, h.config.Iterations, h.config.Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether a successful verification should be rehashed. Higher
// stored costs are preserved: rehash never reduces memory or iteration costs.
func (h *PasswordHasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	cost, salt, want, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}
	actual, err := h.derive(ctx, password, salt, cost)
	if err != nil {
		return false, err
	}
	defer clear(actual)
	if subtle.ConstantTimeCompare(actual, want) != 1 {
		return false, ErrInvalidCredentials
	}
	return cost.MemoryKiB <= h.config.MemoryKiB && cost.Iterations <= h.config.Iterations &&
		(cost.MemoryKiB < h.config.MemoryKiB || cost.Iterations < h.config.Iterations), nil
}

func parsePasswordHash(encoded string) (PasswordConfig, []byte, []byte, error) {
	invalid := func() (PasswordConfig, []byte, []byte, error) {
		return PasswordConfig{}, nil, nil, ErrInvalidPasswordHash
	}
	if len(encoded) > 256 {
		return invalid()
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return invalid()
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return invalid()
	}
	values := [3]uint64{}
	for i, prefix := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(parameters[i], prefix) {
			return invalid()
		}
		value := strings.TrimPrefix(parameters[i], prefix)
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || strconv.FormatUint(parsed, 10) != value {
			return invalid()
		}
		values[i] = parsed
	}
	if values[2] > 255 {
		return invalid()
	}
	cost := PasswordConfig{MemoryKiB: uint32(values[0]), Iterations: uint32(values[1]), Parallelism: uint8(values[2])}
	if !validPasswordCost(cost) {
		return invalid()
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) != 16 || base64.RawStdEncoding.EncodeToString(salt) != parts[4] {
		return invalid()
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) != 32 || base64.RawStdEncoding.EncodeToString(key) != parts[5] {
		return invalid()
	}
	return cost, salt, key, nil
}
