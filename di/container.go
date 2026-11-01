// Package di предоставляет контейнер внедрения зависимостей.
package di

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

var errorType = reflect.TypeFor[error]()

// Container хранит определения сервисов и создаёт граф зависимостей.
type Container struct {
	mu      sync.RWMutex
	defs    map[reflect.Type]*definition
	aliases map[reflect.Type]reflect.Type
}

type definition struct {
	constructor reflect.Value
	instance    reflect.Value
	singleton   bool
	once        sync.Once
	err         error
}

// Option изменяет способ регистрации конструктора.
type Option func(*definition)

// Singleton указывает контейнеру создать сервис только один раз.
func Singleton() Option {
	return func(def *definition) { def.singleton = true }
}

// New создаёт пустой контейнер.
func New() *Container {
	return &Container{
		defs:    make(map[reflect.Type]*definition),
		aliases: make(map[reflect.Type]reflect.Type),
	}
}

// Provide регистрирует конструктор вида func(A, B) T или func(A, B) (T, error).
func (c *Container) Provide(constructor any, options ...Option) error {
	value := reflect.ValueOf(constructor)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return errors.New("di: конструктор должен быть функцией")
	}

	typeOf := value.Type()
	if typeOf.NumOut() < 1 || typeOf.NumOut() > 2 ||
		(typeOf.NumOut() == 2 && typeOf.Out(1) != errorType) {
		return errors.New("di: конструктор должен возвращать T или (T, error)")
	}

	def := &definition{constructor: value}
	for _, option := range options {
		option(def)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	resultType := typeOf.Out(0)
	if _, exists := c.defs[resultType]; exists {
		return fmt.Errorf("di: тип %s уже зарегистрирован", resultType)
	}
	c.defs[resultType] = def
	return nil
}

// Instance регистрирует уже созданный singleton.
func (c *Container) Instance(instance any) error {
	value := reflect.ValueOf(instance)
	if !value.IsValid() {
		return errors.New("di: нельзя зарегистрировать nil без типа")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.defs[value.Type()]; exists {
		return fmt.Errorf("di: тип %s уже зарегистрирован", value.Type())
	}
	c.defs[value.Type()] = &definition{instance: value, singleton: true}
	return nil
}

// Bind связывает интерфейс с зарегистрированной или автоматически создаваемой реализацией.
// Интерфейс передаётся как (*MyInterface)(nil), реализация — как (*MyService)(nil).
func (c *Container) Bind(abstraction, implementation any) error {
	abstractType, err := interfaceTokenType(abstraction)
	if err != nil {
		return err
	}
	implementationType, err := concreteTokenType(implementation)
	if err != nil {
		return err
	}
	if !implementationType.Implements(abstractType) {
		return fmt.Errorf("di: %s не реализует %s", implementationType, abstractType)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.aliases[abstractType] = implementationType
	return nil
}

// Resolve записывает найденный сервис в указатель target.
func (c *Container) Resolve(target any) error {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return errors.New("di: target должен быть ненулевым указателем")
	}
	resolved, err := c.resolve(value.Elem().Type(), nil)
	if err != nil {
		return err
	}
	value.Elem().Set(resolved)
	return nil
}

// Invoke вызывает функцию, разрешая её аргументы через контейнер.
func (c *Container) Invoke(function any) ([]any, error) {
	value := reflect.ValueOf(function)
	if !value.IsValid() || value.Kind() != reflect.Func {
		return nil, errors.New("di: Invoke принимает функцию")
	}
	results, err := c.call(value, nil)
	if err != nil {
		return nil, err
	}
	items := make([]any, len(results))
	for i, result := range results {
		items[i] = result.Interface()
	}
	return items, nil
}

func (c *Container) resolve(requested reflect.Type, chain []reflect.Type) (reflect.Value, error) {
	actual := requested
	c.mu.RLock()
	if alias, exists := c.aliases[requested]; exists {
		actual = alias
	}
	def := c.defs[actual]
	c.mu.RUnlock()

	for _, item := range chain {
		if item == actual {
			path := append(append([]reflect.Type{}, chain...), actual)
			names := make([]string, len(path))
			for i, dependency := range path {
				names[i] = dependency.String()
			}
			return reflect.Value{}, fmt.Errorf("di: циклическая зависимость: %s", strings.Join(names, " -> "))
		}
	}
	chain = append(chain, actual)

	if def == nil {
		return c.autowire(actual, chain)
	}
	if def.instance.IsValid() {
		return adaptValue(def.instance, requested)
	}
	if def.singleton {
		def.once.Do(func() {
			def.instance, def.err = c.construct(def.constructor, chain)
		})
		if def.err != nil {
			return reflect.Value{}, def.err
		}
		return adaptValue(def.instance, requested)
	}

	instance, err := c.construct(def.constructor, chain)
	if err != nil {
		return reflect.Value{}, err
	}
	return adaptValue(instance, requested)
}

func (c *Container) construct(constructor reflect.Value, chain []reflect.Type) (reflect.Value, error) {
	results, err := c.call(constructor, chain)
	if err != nil {
		return reflect.Value{}, err
	}
	if len(results) == 2 && !results[1].IsNil() {
		return reflect.Value{}, results[1].Interface().(error)
	}
	return results[0], nil
}

func (c *Container) call(function reflect.Value, chain []reflect.Type) ([]reflect.Value, error) {
	typeOf := function.Type()
	arguments := make([]reflect.Value, typeOf.NumIn())
	for i := range arguments {
		dependency, err := c.resolve(typeOf.In(i), chain)
		if err != nil {
			return nil, fmt.Errorf("di: аргумент %d функции %s: %w", i, typeOf, err)
		}
		arguments[i] = dependency
	}
	return function.Call(arguments), nil
}

func (c *Container) autowire(typeOf reflect.Type, chain []reflect.Type) (reflect.Value, error) {
	if typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("di: нет определения для %s", typeOf)
	}

	value := reflect.New(typeOf.Elem())
	structure := typeOf.Elem()
	for i := 0; i < structure.NumField(); i++ {
		fieldInfo := structure.Field(i)
		if fieldInfo.Tag.Get("di") != "inject" {
			continue
		}
		field := value.Elem().Field(i)
		if !field.CanSet() {
			return reflect.Value{}, fmt.Errorf("di: поле %s.%s недоступно для внедрения", structure, fieldInfo.Name)
		}
		dependency, err := c.resolve(fieldInfo.Type, chain)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("di: поле %s.%s: %w", structure, fieldInfo.Name, err)
		}
		field.Set(dependency)
	}
	return value, nil
}

func adaptValue(value reflect.Value, requested reflect.Type) (reflect.Value, error) {
	if value.Type().AssignableTo(requested) {
		return value, nil
	}
	if value.Type().Implements(requested) {
		return value, nil
	}
	return reflect.Value{}, fmt.Errorf("di: %s нельзя присвоить %s", value.Type(), requested)
}

func interfaceTokenType(token any) (reflect.Type, error) {
	typeOf := reflect.TypeOf(token)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Interface {
		return nil, errors.New("di: интерфейс должен передаваться как (*MyInterface)(nil)")
	}
	return typeOf.Elem(), nil
}

func concreteTokenType(token any) (reflect.Type, error) {
	typeOf := reflect.TypeOf(token)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return nil, errors.New("di: реализация должна передаваться как (*MyService)(nil)")
	}
	return typeOf, nil
}
