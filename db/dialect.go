// Package db предоставляет слой данных Chitu поверх database/sql.
package db

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// PostgreSQL определяет имя встроенного диалекта PostgreSQL.
	PostgreSQL = "postgresql"
	// MySQL определяет имя общего диалекта MySQL и MariaDB.
	MySQL = "mysql"
	// Oracle определяет имя встроенного диалекта Oracle.
	Oracle = "oracle"
	// SQLServer определяет имя встроенного диалекта Microsoft SQL Server.
	SQLServer = "sqlserver"
)

// Dialect описывает синтаксические различия SQL, необходимые построителю запросов.
// Имя должно оставаться стабильным на протяжении жизни зарегистрированного объекта.
type Dialect interface {
	Name() string
	QuoteIdentifier(identifier string) string
	Placeholder(position int) string
}

// SavepointDialect расширяет Dialect синтаксисом вложенных транзакций.
// Пустой результат ReleaseSavepointSQL означает, что СУБД не поддерживает
// явное освобождение savepoint и он живёт до завершения внешней транзакции.
type SavepointDialect interface {
	Dialect
	SavepointSQL(name string) string
	RollbackToSavepointSQL(name string) string
	ReleaseSavepointSQL(name string) string
}

// DialectRegistry хранит диалекты по регистронезависимому имени.
type DialectRegistry struct {
	mu       sync.RWMutex
	dialects map[string]Dialect
}

// NewDialectRegistry создаёт реестр и регистрирует переданные диалекты.
func NewDialectRegistry(dialects ...Dialect) (*DialectRegistry, error) {
	registry := &DialectRegistry{dialects: make(map[string]Dialect)}
	for _, dialect := range dialects {
		if err := registry.Register(dialect); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// NewDefaultDialectRegistry создаёт независимый реестр встроенных диалектов.
func NewDefaultDialectRegistry() *DialectRegistry {
	registry := &DialectRegistry{dialects: make(map[string]Dialect)}
	for _, dialect := range BuiltinDialects() {
		registry.dialects[dialect.Name()] = dialect
	}
	return registry
}

// BuiltinDialects возвращает новый срез встроенных диалектов.
func BuiltinDialects() []Dialect {
	return []Dialect{
		builtinDialect{name: PostgreSQL, openQuote: `"`, closeQuote: `"`, placeholder: dollarPlaceholder},
		builtinDialect{name: MySQL, openQuote: "`", closeQuote: "`", placeholder: questionPlaceholder},
		builtinDialect{name: Oracle, openQuote: `"`, closeQuote: `"`, placeholder: colonPlaceholder},
		builtinDialect{name: SQLServer, openQuote: "[", closeQuote: "]", placeholder: namedPlaceholder},
	}
}

// Register добавляет диалект. Повторная регистрация имени запрещена.
func (registry *DialectRegistry) Register(dialect Dialect) error {
	if registry == nil {
		return errors.New("db: реестр диалектов не задан")
	}
	if dialect == nil || isNilDialect(dialect) {
		return errors.New("db: диалект не может быть nil")
	}
	name := normalizeDialectName(dialect.Name())
	if name == "" {
		return errors.New("db: имя диалекта не может быть пустым")
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.dialects == nil {
		registry.dialects = make(map[string]Dialect)
	}
	if _, exists := registry.dialects[name]; exists {
		return fmt.Errorf("db: диалект %q уже зарегистрирован", name)
	}
	registry.dialects[name] = dialect
	return nil
}

// Lookup находит диалект по имени.
func (registry *DialectRegistry) Lookup(name string) (Dialect, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	dialect, exists := registry.dialects[normalizeDialectName(name)]
	registry.mu.RUnlock()
	return dialect, exists
}

// Require возвращает диалект или описательную ошибку.
func (registry *DialectRegistry) Require(name string) (Dialect, error) {
	dialect, exists := registry.Lookup(name)
	if !exists {
		return nil, fmt.Errorf("db: диалект %q не зарегистрирован", name)
	}
	return dialect, nil
}

// Names возвращает отсортированные нормализованные имена диалектов.
func (registry *DialectRegistry) Names() []string {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	names := make([]string, 0, len(registry.dialects))
	for name := range registry.dialects {
		names = append(names, name)
	}
	registry.mu.RUnlock()
	sort.Strings(names)
	return names
}

type builtinDialect struct {
	name        string
	openQuote   string
	closeQuote  string
	placeholder func(int) string
}

func (dialect builtinDialect) Name() string { return dialect.name }

func (dialect builtinDialect) QuoteIdentifier(identifier string) string {
	escaped := strings.ReplaceAll(identifier, dialect.closeQuote, dialect.closeQuote+dialect.closeQuote)
	return dialect.openQuote + escaped + dialect.closeQuote
}

func (dialect builtinDialect) Placeholder(position int) string {
	return dialect.placeholder(position)
}

func (dialect builtinDialect) SavepointSQL(name string) string {
	if dialect.name == SQLServer {
		return "SAVE TRANSACTION " + name
	}
	return "SAVEPOINT " + name
}

func (dialect builtinDialect) RollbackToSavepointSQL(name string) string {
	switch dialect.name {
	case SQLServer:
		return "ROLLBACK TRANSACTION " + name
	case Oracle:
		return "ROLLBACK TO " + name
	default:
		return "ROLLBACK TO SAVEPOINT " + name
	}
}

func (dialect builtinDialect) ReleaseSavepointSQL(name string) string {
	if dialect.name == Oracle || dialect.name == SQLServer {
		return ""
	}
	return "RELEASE SAVEPOINT " + name
}

func dollarPlaceholder(position int) string { return "$" + strconv.Itoa(position) }
func colonPlaceholder(position int) string  { return ":" + strconv.Itoa(position) }
func namedPlaceholder(position int) string  { return "@p" + strconv.Itoa(position) }
func questionPlaceholder(int) string        { return "?" }

func normalizeDialectName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isNilDialect(dialect Dialect) bool {
	value := reflect.ValueOf(dialect)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
