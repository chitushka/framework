// Package config загружает конфигурацию приложения из файлов и окружения.
package config

import (
	"bufio"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const maxConfigFileSize = 4 << 20

var (
	durationType        = reflect.TypeFor[time.Duration]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// Option настраивает источники Loader.
type Option func(*Loader)

// WithJSONFile добавляет JSON-файл. Файлы применяются в порядке объявления.
func WithJSONFile(path string) Option {
	return func(loader *Loader) {
		loader.jsonFiles = append(loader.jsonFiles, path)
	}
}

// WithEnvFile добавляет .env-файл. Последний файл имеет больший приоритет.
func WithEnvFile(path string) Option {
	return func(loader *Loader) {
		loader.envFiles = append(loader.envFiles, path)
	}
}

// WithEnvironment заменяет системное окружение заданными значениями.
// Опция предназначена также для детерминированных тестов и изоляции процессов.
func WithEnvironment(values map[string]string) Option {
	copyOfValues := cloneMap(values)
	return func(loader *Loader) {
		loader.environment = copyOfValues
		loader.useSystemEnvironment = false
	}
}

// Loader последовательно объединяет конфигурационные источники.
// Приоритет: JSON-файлы, .env-файлы, окружение процесса.
type Loader struct {
	jsonFiles            []string
	envFiles             []string
	environment          map[string]string
	useSystemEnvironment bool
}

// New создаёт загрузчик конфигурации.
func New(options ...Option) *Loader {
	loader := &Loader{useSystemEnvironment: true}
	for _, option := range options {
		if option != nil {
			option(loader)
		}
	}
	return loader
}

// Load заполняет target. Target должен быть ненулевым указателем на структуру.
// Поля окружения задаются тегами env, envDefault и envRequired.
func (l *Loader) Load(target any) error {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return errors.New("config: target должен быть ненулевым указателем на структуру")
	}

	for _, path := range l.jsonFiles {
		if err := loadJSON(path, target); err != nil {
			return err
		}
	}

	values := make(map[string]string)
	for _, path := range l.envFiles {
		fileValues, err := loadEnvFile(path)
		if err != nil {
			return err
		}
		merge(values, fileValues)
	}
	if l.useSystemEnvironment {
		merge(values, systemEnvironment())
	} else {
		merge(values, l.environment)
	}

	return applyEnvironment(value.Elem(), values, value.Elem().Type().Name())
}

func loadJSON(path string, target any) error {
	if path == "" {
		return errors.New("config: путь к JSON-файлу не может быть пустым")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("config: открыть JSON-файл %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("config: получить информацию о JSON-файле %q: %w", path, err)
	}
	if info.Size() > maxConfigFileSize {
		return fmt.Errorf("config: JSON-файл %q превышает лимит %d байт", path, maxConfigFileSize)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxConfigFileSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("config: декодировать JSON-файл %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("config: JSON-файл %q должен содержать один объект", path)
	}
	return nil
}

func loadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, errors.New("config: путь к .env-файлу не может быть пустым")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: открыть .env-файл %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("config: получить информацию о .env-файле %q: %w", path, err)
	}
	if info.Size() > maxConfigFileSize {
		return nil, fmt.Errorf("config: .env-файл %q превышает лимит %d байт", path, maxConfigFileSize)
	}

	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(file, maxConfigFileSize+1))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		key, value, ok, err := parseEnvLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("config: %s:%d: %w", path, lineNumber, err)
		}
		if ok {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("config: прочитать .env-файл %q: %w", path, err)
	}
	return values, nil
}

func parseEnvLine(line string) (key, value string, ok bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false, nil
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	key, value, found := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !found || !validEnvName(key) {
		return "", "", false, errors.New("некорректное объявление переменной")
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return key, value[1 : len(value)-1], true, nil
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		unquoted, unquoteErr := strconv.Unquote(value)
		if unquoteErr != nil {
			return "", "", false, fmt.Errorf("некорректное quoted-значение: %w", unquoteErr)
		}
		return key, unquoted, true, nil
	}
	if index := strings.Index(value, " #"); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return key, value, true, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, char := range name {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || i > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func applyEnvironment(value reflect.Value, values map[string]string, path string) error {
	typeOf := value.Type()
	for i := 0; i < value.NumField(); i++ {
		fieldInfo := typeOf.Field(i)
		field := value.Field(i)
		fieldPath := fieldInfo.Name
		if path != "" {
			fieldPath = path + "." + fieldInfo.Name
		}

		envName := fieldInfo.Tag.Get("env")
		if envName == "-" {
			continue
		}
		if envName == "" && field.Kind() == reflect.Struct && field.Type() != durationType {
			if err := applyEnvironment(field, values, fieldPath); err != nil {
				return err
			}
			continue
		}
		if envName == "" {
			continue
		}
		if !field.CanSet() {
			return fmt.Errorf("config: поле %s недоступно для записи", fieldPath)
		}

		raw, exists := values[envName]
		if !exists && field.IsZero() {
			raw, exists = fieldInfo.Tag.Lookup("envDefault")
		}
		if !exists {
			if fieldInfo.Tag.Get("envRequired") == "true" && field.IsZero() {
				return fmt.Errorf("config: обязательная переменная %s для поля %s не задана", envName, fieldPath)
			}
			continue
		}
		if err := setValue(field, raw); err != nil {
			return fmt.Errorf("config: переменная %s для поля %s: %w", envName, fieldPath, err)
		}
	}
	return nil
}

func setValue(value reflect.Value, raw string) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		return setValue(value.Elem(), raw)
	}
	if value.CanAddr() && value.Addr().Type().Implements(textUnmarshalerType) {
		return value.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText([]byte(raw))
	}
	if value.Type() == durationType {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		value.SetInt(int64(parsed))
		return nil
	}

	switch value.Kind() {
	case reflect.String:
		value.SetString(raw)
	case reflect.Bool:
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		value.SetBool(parsed)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		parsed, err := strconv.ParseInt(raw, 10, value.Type().Bits())
		if err != nil {
			return err
		}
		value.SetInt(parsed)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		parsed, err := strconv.ParseUint(raw, 10, value.Type().Bits())
		if err != nil {
			return err
		}
		value.SetUint(parsed)
	case reflect.Float32, reflect.Float64:
		parsed, err := strconv.ParseFloat(raw, value.Type().Bits())
		if err != nil {
			return err
		}
		value.SetFloat(parsed)
	case reflect.Slice:
		parts := splitList(raw)
		slice := reflect.MakeSlice(value.Type(), len(parts), len(parts))
		for i, part := range parts {
			if err := setValue(slice.Index(i), part); err != nil {
				return fmt.Errorf("элемент %d: %w", i, err)
			}
		}
		value.Set(slice)
	default:
		return fmt.Errorf("тип %s не поддерживается", value.Type())
	}
	return nil
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func systemEnvironment() map[string]string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, found := strings.Cut(item, "=")
		if found {
			values[key] = value
		}
	}
	return values
}

func merge(destination, source map[string]string) {
	for key, value := range source {
		destination[key] = value
	}
}

func cloneMap(source map[string]string) map[string]string {
	copyOfSource := make(map[string]string, len(source))
	merge(copyOfSource, source)
	return copyOfSource
}
