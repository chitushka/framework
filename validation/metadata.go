package validation

import (
	"fmt"
	"reflect"
	"strings"
	"unicode"
)

type modelField struct {
	index []int
	name  string
	type_ reflect.Type
}

type modelMetadata struct {
	byName map[string]modelField
	fields []modelField
}

func inspectModel(model any) (*modelMetadata, reflect.Value, error) {
	value := reflect.ValueOf(model)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return nil, reflect.Value{}, errorsModelPointer()
	}
	value = value.Elem()
	metadata := &modelMetadata{byName: make(map[string]modelField)}
	if err := collectModelFields(metadata, value.Type(), nil); err != nil {
		return nil, reflect.Value{}, err
	}
	return metadata, value, nil
}

func errorsModelPointer() error {
	return fmt.Errorf("validation: model must be a non-nil pointer to struct")
}

func collectModelFields(metadata *modelMetadata, type_ reflect.Type, prefix []int) error {
	for i := 0; i < type_.NumField(); i++ {
		field := type_.Field(i)
		if field.PkgPath != "" {
			continue
		}
		index := append(append([]int(nil), prefix...), i)
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			if err := collectModelFields(metadata, field.Type, index); err != nil {
				return err
			}
			continue
		}
		name := externalName(field)
		if name == "-" {
			continue
		}
		if _, found := metadata.byName[name]; found {
			return fmt.Errorf("validation: duplicate field name %q", name)
		}
		item := modelField{index: index, name: name, type_: field.Type}
		metadata.byName[name] = item
		metadata.fields = append(metadata.fields, item)
	}
	return nil
}

func externalName(field reflect.StructField) string {
	for _, tagName := range []string{"form", "json"} {
		tag := field.Tag.Get(tagName)
		if tag == "-" {
			return "-"
		}
		if name := strings.Split(tag, ",")[0]; name != "" {
			return name
		}
	}
	return snakeName(field.Name)
}

func snakeName(value string) string {
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
