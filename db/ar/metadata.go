// Package ar provides a type-safe Active Record repository for Go structs.
package ar

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"
)

var metadataCache sync.Map

type fieldMetadata struct {
	index   []int
	name    string
	type_   reflect.Type
	primary bool
	auto    bool
	version bool
}

type modelMetadata struct {
	type_   reflect.Type
	fields  []fieldMetadata
	byName  map[string]int
	primary []int
	auto    int
	version int
}

func metadataFor[T any]() (*modelMetadata, error) {
	type_ := reflect.TypeFor[T]()
	if type_.Kind() == reflect.Pointer {
		return nil, errors.New("ar: model type must be a struct, not a pointer")
	}
	if type_.Kind() != reflect.Struct {
		return nil, fmt.Errorf("ar: model type %s is not a struct", type_)
	}
	if cached, found := metadataCache.Load(type_); found {
		return cached.(*modelMetadata), nil
	}
	metadata := &modelMetadata{type_: type_, byName: make(map[string]int), auto: -1, version: -1}
	if err := collectFields(metadata, type_, nil); err != nil {
		return nil, err
	}
	if len(metadata.fields) == 0 {
		return nil, fmt.Errorf("ar: model %s has no database fields", type_)
	}
	actual, _ := metadataCache.LoadOrStore(type_, metadata)
	return actual.(*modelMetadata), nil
}

func collectFields(metadata *modelMetadata, type_ reflect.Type, prefix []int) error {
	for i := 0; i < type_.NumField(); i++ {
		field := type_.Field(i)
		if field.PkgPath != "" {
			continue
		}
		index := append(append([]int(nil), prefix...), i)
		tag := field.Tag.Get("db")
		if tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" && field.Type.Kind() == reflect.Struct {
			if err := collectFields(metadata, field.Type, index); err != nil {
				return err
			}
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = snakeCase(field.Name)
		}
		if _, exists := metadata.byName[name]; exists {
			return fmt.Errorf("ar: duplicate column %q in %s", name, metadata.type_)
		}
		item := fieldMetadata{index: index, name: name, type_: field.Type}
		for _, option := range parts[1:] {
			switch strings.TrimSpace(option) {
			case "":
			case "primary":
				item.primary = true
			case "auto":
				item.auto = true
			case "version":
				item.version = true
			default:
				return fmt.Errorf("ar: unknown db tag option %q on %s.%s", option, metadata.type_, field.Name)
			}
		}
		position := len(metadata.fields)
		metadata.fields = append(metadata.fields, item)
		metadata.byName[name] = position
		if item.primary {
			metadata.primary = append(metadata.primary, position)
		}
		if item.auto {
			if metadata.auto >= 0 {
				return fmt.Errorf("ar: model %s has multiple auto fields", metadata.type_)
			}
			metadata.auto = position
		}
		if item.version {
			if metadata.version >= 0 {
				return fmt.Errorf("ar: model %s has multiple version fields", metadata.type_)
			}
			if !isInteger(field.Type.Kind()) {
				return fmt.Errorf("ar: version field %s.%s must be an integer", metadata.type_, field.Name)
			}
			metadata.version = position
		}
	}
	return nil
}

func (metadata *modelMetadata) fieldValue(model reflect.Value, position int) reflect.Value {
	return model.FieldByIndex(metadata.fields[position].index)
}

func (metadata *modelMetadata) columnValue(model reflect.Value, column string) (any, error) {
	position, found := metadata.byName[column]
	if !found {
		return nil, fmt.Errorf("ar: model %s has no column %q", metadata.type_, column)
	}
	return metadata.fieldValue(model, position).Interface(), nil
}

func isInteger(kind reflect.Kind) bool {
	return kind >= reflect.Int && kind <= reflect.Int64 || kind >= reflect.Uint && kind <= reflect.Uint64
}

func snakeCase(value string) string {
	runes := []rune(value)
	var result strings.Builder
	for i, char := range runes {
		if unicode.IsUpper(char) {
			if i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				result.WriteByte('_')
			}
			result.WriteRune(unicode.ToLower(char))
		} else {
			result.WriteRune(char)
		}
	}
	return result.String()
}
