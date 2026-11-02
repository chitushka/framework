// Package rbac provides hierarchical role-based access control.
package rbac

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrNotFound      = errors.New("rbac: item not found")
	ErrAlreadyExists = errors.New("rbac: item already exists")
	ErrCycle         = errors.New("rbac: hierarchy cycle")
)

// Kind distinguishes roles from permissions.
type Kind string

const (
	Role       Kind = "role"
	Permission Kind = "permission"
)

// Item is a role or permission. Rule names an optional runtime Rule.
type Item struct {
	Name        string
	Kind        Kind
	Description string
	Rule        string
}

// Assignment binds a role to an application user.
type Assignment struct {
	UserID string
	Role   string
}

// Store persists RBAC items, hierarchy edges and role assignments.
type Store interface {
	Item(context.Context, string) (Item, bool, error)
	Items(context.Context, Kind) ([]Item, error)
	AddItem(context.Context, Item) error
	UpdateItem(context.Context, Item) error
	RemoveItem(context.Context, string) error
	Children(context.Context, string) ([]Item, error)
	AddChild(context.Context, string, string) error
	RemoveChild(context.Context, string, string) error
	Assignments(context.Context, string) ([]Assignment, error)
	Assign(context.Context, Assignment) error
	Revoke(context.Context, Assignment) error
}

// Rule performs contextual authorization for an item. Returning false prunes
// that item and its descendants from the hierarchy traversal.
type Rule interface {
	Execute(context.Context, string, map[string]any) (bool, error)
}

type RuleFunc func(context.Context, string, map[string]any) (bool, error)

func (function RuleFunc) Execute(ctx context.Context, userID string, params map[string]any) (bool, error) {
	return function(ctx, userID, params)
}

// Manager maintains hierarchy invariants and checks permissions.
type Manager struct {
	store Store
	mu    sync.RWMutex
	rules map[string]Rule
	write sync.Mutex
}

func New(store Store) (*Manager, error) {
	if store == nil {
		return nil, errors.New("rbac: store is required")
	}
	return &Manager{store: store, rules: make(map[string]Rule)}, nil
}

// RegisterRule adds a named contextual rule. Names are unique within a manager.
func (manager *Manager) RegisterRule(name string, rule Rule) error {
	if err := validateName(name); err != nil {
		return fmt.Errorf("rbac: rule: %w", err)
	}
	if rule == nil {
		return errors.New("rbac: rule is required")
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, exists := manager.rules[name]; exists {
		return fmt.Errorf("rbac: rule %q: %w", name, ErrAlreadyExists)
	}
	manager.rules[name] = rule
	return nil
}

func (manager *Manager) AddRole(ctx context.Context, name, description string) error {
	return manager.AddItem(ctx, Item{Name: name, Kind: Role, Description: description})
}

func (manager *Manager) AddPermission(ctx context.Context, name, description string) error {
	return manager.AddItem(ctx, Item{Name: name, Kind: Permission, Description: description})
}

func (manager *Manager) AddItem(ctx context.Context, item Item) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := validateItem(item); err != nil {
		return err
	}
	if item.Rule != "" {
		manager.mu.RLock()
		_, exists := manager.rules[item.Rule]
		manager.mu.RUnlock()
		if !exists {
			return fmt.Errorf("rbac: rule %q is not registered", item.Rule)
		}
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	return manager.store.AddItem(ctx, item)
}

// UpdateItem changes description or rule without changing the item's kind.
func (manager *Manager) UpdateItem(ctx context.Context, item Item) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := validateItem(item); err != nil {
		return err
	}
	if item.Rule != "" {
		manager.mu.RLock()
		_, exists := manager.rules[item.Rule]
		manager.mu.RUnlock()
		if !exists {
			return fmt.Errorf("rbac: rule %q is not registered", item.Rule)
		}
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	current, found, err := manager.store.Item(ctx, item.Name)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("rbac: item %q: %w", item.Name, ErrNotFound)
	}
	if current.Kind != item.Kind {
		return errors.New("rbac: item kind cannot be changed")
	}
	return manager.store.UpdateItem(ctx, item)
}

func (manager *Manager) RemoveItem(ctx context.Context, name string) error {
	if err := validateContextName(ctx, name); err != nil {
		return err
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	return manager.store.RemoveItem(ctx, name)
}

func (manager *Manager) Item(ctx context.Context, name string) (Item, bool, error) {
	if err := validateContextName(ctx, name); err != nil {
		return Item{}, false, err
	}
	return manager.store.Item(ctx, name)
}

func (manager *Manager) Roles(ctx context.Context) ([]Item, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	return manager.store.Items(ctx, Role)
}

func (manager *Manager) Permissions(ctx context.Context) ([]Item, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	return manager.store.Items(ctx, Permission)
}

func (manager *Manager) Children(ctx context.Context, parent string) ([]Item, error) {
	if err := validateContextName(ctx, parent); err != nil {
		return nil, err
	}
	return manager.store.Children(ctx, parent)
}

func (manager *Manager) Assignments(ctx context.Context, userID string) ([]Assignment, error) {
	if err := validateContextName(ctx, userID); err != nil {
		return nil, err
	}
	return manager.store.Assignments(ctx, userID)
}

// AddChild makes child reachable through parent while preventing invalid role
// placement and cycles. Permission parents may contain only permissions.
func (manager *Manager) AddChild(ctx context.Context, parentName, childName string) error {
	if err := validateContextName(ctx, parentName); err != nil {
		return err
	}
	if err := validateName(childName); err != nil {
		return err
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	parent, found, err := manager.store.Item(ctx, parentName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("rbac: parent %q: %w", parentName, ErrNotFound)
	}
	child, found, err := manager.store.Item(ctx, childName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("rbac: child %q: %w", childName, ErrNotFound)
	}
	if parent.Kind == Permission && child.Kind == Role {
		return errors.New("rbac: a permission cannot contain a role")
	}
	if parentName == childName {
		return ErrCycle
	}
	reachesParent, err := manager.reaches(ctx, childName, parentName, make(map[string]bool))
	if err != nil {
		return err
	}
	if reachesParent {
		return ErrCycle
	}
	return manager.store.AddChild(ctx, parentName, childName)
}

func (manager *Manager) RemoveChild(ctx context.Context, parent, child string) error {
	if err := validateContextName(ctx, parent); err != nil {
		return err
	}
	if err := validateName(child); err != nil {
		return err
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	return manager.store.RemoveChild(ctx, parent, child)
}

func (manager *Manager) Assign(ctx context.Context, userID, roleName string) error {
	if err := validateContextName(ctx, userID); err != nil {
		return fmt.Errorf("rbac: user: %w", err)
	}
	if err := validateName(roleName); err != nil {
		return err
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	item, found, err := manager.store.Item(ctx, roleName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("rbac: role %q: %w", roleName, ErrNotFound)
	}
	if item.Kind != Role {
		return fmt.Errorf("rbac: %q is not a role", roleName)
	}
	return manager.store.Assign(ctx, Assignment{UserID: userID, Role: roleName})
}

func (manager *Manager) Revoke(ctx context.Context, userID, roleName string) error {
	if err := validateContextName(ctx, userID); err != nil {
		return fmt.Errorf("rbac: user: %w", err)
	}
	if err := validateName(roleName); err != nil {
		return err
	}
	manager.write.Lock()
	defer manager.write.Unlock()
	return manager.store.Revoke(ctx, Assignment{UserID: userID, Role: roleName})
}

// CheckAccess reports whether any assigned role reaches permission and every
// contextual rule on the selected path allows access.
func (manager *Manager) CheckAccess(ctx context.Context, userID, permission string, params map[string]any) (bool, error) {
	if err := validateContextName(ctx, userID); err != nil {
		return false, fmt.Errorf("rbac: user: %w", err)
	}
	if err := validateName(permission); err != nil {
		return false, err
	}
	target, found, err := manager.store.Item(ctx, permission)
	if err != nil || !found {
		return false, err
	}
	if target.Kind != Permission {
		return false, nil
	}
	assignments, err := manager.store.Assignments(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, assignment := range assignments {
		allowed, err := manager.checkFrom(ctx, userID, assignment.Role, permission, params, make(map[string]bool))
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

func (manager *Manager) checkFrom(ctx context.Context, userID, current, target string, params map[string]any, visiting map[string]bool) (bool, error) {
	if visiting[current] {
		return false, ErrCycle
	}
	visiting[current] = true
	defer delete(visiting, current)
	item, found, err := manager.store.Item(ctx, current)
	if err != nil || !found {
		return false, err
	}
	if item.Rule != "" {
		manager.mu.RLock()
		rule := manager.rules[item.Rule]
		manager.mu.RUnlock()
		if rule == nil {
			return false, fmt.Errorf("rbac: rule %q is not registered", item.Rule)
		}
		allowed, err := rule.Execute(ctx, userID, params)
		if err != nil || !allowed {
			return false, err
		}
	}
	if current == target {
		return true, nil
	}
	children, err := manager.store.Children(ctx, current)
	if err != nil {
		return false, err
	}
	for _, child := range children {
		allowed, err := manager.checkFrom(ctx, userID, child.Name, target, params, visiting)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

func (manager *Manager) reaches(ctx context.Context, current, target string, visited map[string]bool) (bool, error) {
	if current == target {
		return true, nil
	}
	if visited[current] {
		return false, nil
	}
	visited[current] = true
	children, err := manager.store.Children(ctx, current)
	if err != nil {
		return false, err
	}
	for _, child := range children {
		found, err := manager.reaches(ctx, child.Name, target, visited)
		if err != nil || found {
			return found, err
		}
	}
	return false, nil
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("rbac: context is nil")
	}
	return ctx.Err()
}

func validateContextName(ctx context.Context, name string) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	return validateName(name)
}

func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name is empty")
	}
	if name != strings.TrimSpace(name) || len(name) > 255 || strings.ContainsRune(name, '\x00') {
		return errors.New("invalid name")
	}
	return nil
}

func validateItem(item Item) error {
	if err := validateName(item.Name); err != nil {
		return fmt.Errorf("rbac: item: %w", err)
	}
	if item.Kind != Role && item.Kind != Permission {
		return fmt.Errorf("rbac: invalid item kind %q", item.Kind)
	}
	if len(item.Description) > 1024 || strings.ContainsRune(item.Description, '\x00') {
		return errors.New("rbac: invalid item description")
	}
	if item.Rule != "" {
		if err := validateName(item.Rule); err != nil {
			return fmt.Errorf("rbac: rule: %w", err)
		}
	}
	return nil
}
