package main

import (
	"context"
	"sync"

	"github.com/chitushka/framework/auth"
)

type identity string

func (id identity) ID() string { return string(id) }

// Replace this configured account and session backend together when adding
// persistence or multiple application nodes. Rehash CAS is process-local.
type accountStore struct {
	username string
	mu       sync.RWMutex
	hash     string
}

func (s *accountStore) FindIdentity(_ context.Context, id string) (auth.Identity, error) {
	if id != s.username {
		return nil, nil
	}
	return identity(id), nil
}
func (s *accountStore) FindCredential(_ context.Context, login string) (*auth.Credential, error) {
	if login != s.username {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &auth.Credential{Identity: identity(s.username), PasswordHash: s.hash}, nil
}
func (s *accountStore) ReplacePasswordHash(_ context.Context, id, old, next string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.username || s.hash != old {
		return false, nil
	}
	s.hash = next
	return true, nil
}
