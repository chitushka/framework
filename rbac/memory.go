package rbac

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// MemoryStore is a concurrency-safe in-process RBAC store.
type MemoryStore struct {
	mu          sync.RWMutex
	items       map[string]Item
	children    map[string]map[string]struct{}
	assignments map[string]map[string]struct{}
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: make(map[string]Item), children: make(map[string]map[string]struct{}), assignments: make(map[string]map[string]struct{})}
}

func (store *MemoryStore) Item(ctx context.Context, name string) (Item, bool, error) {
	if err := validateContext(ctx); err != nil {
		return Item{}, false, err
	}
	store.mu.RLock()
	item, found := store.items[name]
	store.mu.RUnlock()
	return item, found, nil
}

func (store *MemoryStore) Items(ctx context.Context, kind Kind) ([]Item, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	store.mu.RLock()
	result := make([]Item, 0, len(store.items))
	for _, item := range store.items {
		if kind == "" || item.Kind == kind {
			result = append(result, item)
		}
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (store *MemoryStore) AddItem(ctx context.Context, item Item) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.items[item.Name]; exists {
		return fmt.Errorf("rbac: item %q: %w", item.Name, ErrAlreadyExists)
	}
	store.items[item.Name] = item
	return nil
}

func (store *MemoryStore) UpdateItem(ctx context.Context, item Item) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.items[item.Name]; !exists {
		return fmt.Errorf("rbac: item %q: %w", item.Name, ErrNotFound)
	}
	store.items[item.Name] = item
	return nil
}

func (store *MemoryStore) RemoveItem(ctx context.Context, name string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.items[name]; !exists {
		return fmt.Errorf("rbac: item %q: %w", name, ErrNotFound)
	}
	delete(store.items, name)
	delete(store.children, name)
	for parent, children := range store.children {
		delete(children, name)
		if len(children) == 0 {
			delete(store.children, parent)
		}
	}
	for user, roles := range store.assignments {
		delete(roles, name)
		if len(roles) == 0 {
			delete(store.assignments, user)
		}
	}
	return nil
}

func (store *MemoryStore) Children(ctx context.Context, parent string) ([]Item, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	store.mu.RLock()
	names := store.children[parent]
	result := make([]Item, 0, len(names))
	for name := range names {
		if item, found := store.items[name]; found {
			result = append(result, item)
		}
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (store *MemoryStore) AddChild(ctx context.Context, parent, child string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	children := store.children[parent]
	if children == nil {
		children = make(map[string]struct{})
		store.children[parent] = children
	}
	if _, exists := children[child]; exists {
		return fmt.Errorf("rbac: child %q of %q: %w", child, parent, ErrAlreadyExists)
	}
	children[child] = struct{}{}
	return nil
}

func (store *MemoryStore) RemoveChild(ctx context.Context, parent, child string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	children := store.children[parent]
	if _, exists := children[child]; !exists {
		return ErrNotFound
	}
	delete(children, child)
	if len(children) == 0 {
		delete(store.children, parent)
	}
	return nil
}

func (store *MemoryStore) Assignments(ctx context.Context, userID string) ([]Assignment, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	store.mu.RLock()
	roles := store.assignments[userID]
	result := make([]Assignment, 0, len(roles))
	for role := range roles {
		result = append(result, Assignment{UserID: userID, Role: role})
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Role < result[j].Role })
	return result, nil
}

func (store *MemoryStore) Assign(ctx context.Context, assignment Assignment) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	roles := store.assignments[assignment.UserID]
	if roles == nil {
		roles = make(map[string]struct{})
		store.assignments[assignment.UserID] = roles
	}
	if _, exists := roles[assignment.Role]; exists {
		return fmt.Errorf("rbac: assignment: %w", ErrAlreadyExists)
	}
	roles[assignment.Role] = struct{}{}
	return nil
}

func (store *MemoryStore) Revoke(ctx context.Context, assignment Assignment) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	roles := store.assignments[assignment.UserID]
	if _, exists := roles[assignment.Role]; !exists {
		return ErrNotFound
	}
	delete(roles, assignment.Role)
	if len(roles) == 0 {
		delete(store.assignments, assignment.UserID)
	}
	return nil
}
