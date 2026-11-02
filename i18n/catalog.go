package i18n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

type Form string

const (
	Other Form = "other"
	Zero  Form = "zero"
	One   Form = "one"
	Two   Form = "two"
	Few   Form = "few"
	Many  Form = "many"
)

// Message contains plural forms. A simple message uses only Other.
type Message struct{ Forms map[Form]string }

func Text(value string) Message { return Message{Forms: map[Form]string{Other: value}} }
func Plural(forms map[Form]string) Message {
	copy := make(map[Form]string, len(forms))
	for form, value := range forms {
		copy[form] = value
	}
	return Message{Forms: copy}
}

type Catalog interface {
	Lookup(context.Context, Locale, string, string) (Message, bool, error)
}

type MemoryCatalog struct {
	mu      sync.RWMutex
	locales map[Locale]map[string]map[string]Message
}

func NewMemoryCatalog() *MemoryCatalog {
	return &MemoryCatalog{locales: make(map[Locale]map[string]map[string]Message)}
}
func (catalog *MemoryCatalog) Add(locale Locale, category string, messages map[string]Message) error {
	normalized, err := ParseLocale(locale.String())
	if err != nil {
		return err
	}
	locale = normalized
	if err := validateSegment("category", category); err != nil {
		return err
	}
	copied := make(map[string]Message, len(messages))
	for key, message := range messages {
		if err := validateSegment("message key", key); err != nil {
			return err
		}
		if err := validateMessageForms(message); err != nil {
			return fmt.Errorf("i18n: message %q: %w", key, err)
		}
		copied[key] = Plural(message.Forms)
	}
	catalog.mu.Lock()
	if catalog.locales[locale] == nil {
		catalog.locales[locale] = make(map[string]map[string]Message)
	}
	catalog.locales[locale][category] = copied
	catalog.mu.Unlock()
	return nil
}
func (catalog *MemoryCatalog) Lookup(ctx context.Context, locale Locale, category, key string) (Message, bool, error) {
	if ctx == nil {
		return Message{}, false, errors.New("i18n: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return Message{}, false, err
	}
	catalog.mu.RLock()
	message, found := catalog.locales[locale][category][key]
	catalog.mu.RUnlock()
	if !found {
		return Message{}, false, nil
	}
	return Plural(message.Forms), true, nil
}

// LoadFS loads <locale>/<category>.json files. Values are strings or objects
// whose keys are CLDR plural forms (one, few, many, other, ...).
func LoadFS(filesystem fs.FS) (*MemoryCatalog, error) {
	if filesystem == nil {
		return nil, errors.New("i18n: catalog filesystem is required")
	}
	catalog := NewMemoryCatalog()
	found := false
	err := fs.WalkDir(filesystem, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filePath) != ".json" {
			return nil
		}
		parts := strings.Split(path.Clean(filePath), "/")
		if len(parts) != 2 {
			return fmt.Errorf("i18n: catalog path %q must be <locale>/<category>.json", filePath)
		}
		locale, err := ParseLocale(parts[0])
		if err != nil {
			return err
		}
		category := strings.TrimSuffix(parts[1], ".json")
		source, err := fs.ReadFile(filesystem, filePath)
		if err != nil {
			return err
		}
		messages, err := decodeMessages(source)
		if err != nil {
			return fmt.Errorf("i18n: decode %q: %w", filePath, err)
		}
		if err := catalog.Add(locale, category, messages); err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("i18n: no JSON catalogs found")
	}
	return catalog, nil
}

func decodeMessages(source []byte) (map[string]Message, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(source, &values); err != nil {
		return nil, err
	}
	messages := make(map[string]Message, len(values))
	for key, raw := range values {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			messages[key] = Text(text)
			continue
		}
		var forms map[Form]string
		if err := json.Unmarshal(raw, &forms); err != nil {
			return nil, fmt.Errorf("message %q must be a string or plural object", key)
		}
		messages[key] = Plural(forms)
	}
	return messages, nil
}
func validateMessageForms(message Message) error {
	if len(message.Forms) == 0 {
		return errors.New("forms are empty")
	}
	if _, found := message.Forms[Other]; !found {
		return errors.New("other form is required")
	}
	valid := map[Form]bool{Zero: true, One: true, Two: true, Few: true, Many: true, Other: true}
	for form := range message.Forms {
		if !valid[form] {
			return fmt.Errorf("unknown plural form %q", form)
		}
	}
	return nil
}
func validateSegment(kind, value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("i18n: invalid %s %q", kind, value)
	}
	return nil
}
func (catalog *MemoryCatalog) Locales() []Locale {
	catalog.mu.RLock()
	result := make([]Locale, 0, len(catalog.locales))
	for locale := range catalog.locales {
		result = append(result, locale)
	}
	catalog.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
