package i18n

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	texttemplate "text/template"
)

type Config struct {
	DefaultLocale    string
	SupportedLocales []string
	Fallbacks        map[string][]string
	ErrorOnMissing   bool
}
type Translator struct {
	catalog        Catalog
	matcher        *Matcher
	fallback       map[Locale][]Locale
	errorOnMissing bool
}

func New(catalog Catalog, config Config) (*Translator, error) {
	if catalog == nil {
		return nil, errors.New("i18n: catalog is required")
	}
	if config.DefaultLocale == "" {
		return nil, errors.New("i18n: default locale is required")
	}
	if len(config.SupportedLocales) == 0 {
		config.SupportedLocales = []string{config.DefaultLocale}
	}
	matcher, err := NewMatcher(config.SupportedLocales, config.DefaultLocale)
	if err != nil {
		return nil, err
	}
	translator := &Translator{catalog: catalog, matcher: matcher, fallback: make(map[Locale][]Locale), errorOnMissing: config.ErrorOnMissing}
	for source, values := range config.Fallbacks {
		sourceLocale, err := ParseLocale(source)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			locale, err := ParseLocale(value)
			if err != nil {
				return nil, err
			}
			translator.fallback[sourceLocale] = append(translator.fallback[sourceLocale], locale)
		}
	}
	return translator, nil
}

type localeContextKey struct{}

func WithLocale(ctx context.Context, value string) (context.Context, error) {
	if ctx == nil {
		return nil, errors.New("i18n: context is nil")
	}
	locale, err := ParseLocale(value)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, localeContextKey{}, locale), nil
}
func LocaleFromContext(ctx context.Context) (Locale, bool) {
	if ctx == nil {
		return "", false
	}
	locale, found := ctx.Value(localeContextKey{}).(Locale)
	return locale, found
}
func (translator *Translator) Locale(ctx context.Context) Locale {
	if locale, found := LocaleFromContext(ctx); found {
		return translator.matcher.Match(locale)
	}
	return translator.matcher.Fallback()
}
func (translator *Translator) Match(preferred ...Locale) Locale {
	return translator.matcher.Match(preferred...)
}
func (translator *Translator) MatchFound(preferred ...Locale) (Locale, bool) {
	return translator.matcher.MatchFound(preferred...)
}

func (translator *Translator) T(ctx context.Context, category, key string, parameters map[string]any) (string, error) {
	return translator.translate(ctx, translator.Locale(ctx), category, key, Other, parameters)
}
func (translator *Translator) N(ctx context.Context, category, key string, count int64, parameters map[string]any) (string, error) {
	values := make(map[string]any, len(parameters)+1)
	for name, value := range parameters {
		values[name] = value
	}
	values["count"] = count
	return translator.translate(ctx, translator.Locale(ctx), category, key, pluralForm(translator.Locale(ctx), count), values)
}
func (translator *Translator) Translate(locale Locale, category, key string, parameters map[string]any) (string, error) {
	return translator.translate(context.Background(), locale, category, key, Other, parameters)
}
func (translator *Translator) Plural(locale Locale, category, key string, count int64, parameters map[string]any) (string, error) {
	values := make(map[string]any, len(parameters)+1)
	for name, value := range parameters {
		values[name] = value
	}
	values["count"] = count
	return translator.translate(context.Background(), locale, category, key, pluralForm(locale, count), values)
}

func (translator *Translator) translate(ctx context.Context, locale Locale, category, key string, form Form, parameters map[string]any) (string, error) {
	if ctx == nil {
		return "", errors.New("i18n: context is nil")
	}
	if err := validateSegment("category", category); err != nil {
		return "", err
	}
	if err := validateSegment("message key", key); err != nil {
		return "", err
	}
	normalized, err := ParseLocale(locale.String())
	if err != nil {
		return "", err
	}
	for _, candidate := range translator.chain(normalized) {
		message, found, err := translator.catalog.Lookup(ctx, candidate, category, key)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		source, found := message.Forms[form]
		if !found {
			source = message.Forms[Other]
		}
		return executeMessage(category+"."+key, source, parameters)
	}
	if translator.errorOnMissing {
		return "", fmt.Errorf("i18n: message %s.%s not found for %s", category, key, locale)
	}
	return key, nil
}

func (translator *Translator) chain(locale Locale) []Locale {
	result := make([]Locale, 0, 8)
	seen := make(map[Locale]bool)
	add := func(value Locale) {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	add(locale)
	for _, parent := range locale.Parents() {
		add(parent)
	}
	for _, source := range append([]Locale{locale}, locale.Parents()...) {
		for _, fallback := range translator.fallback[source] {
			add(fallback)
			for _, parent := range fallback.Parents() {
				add(parent)
			}
		}
	}
	defaultLocale := translator.matcher.Fallback()
	add(defaultLocale)
	for _, parent := range defaultLocale.Parents() {
		add(parent)
	}
	return result
}
func executeMessage(name, source string, parameters map[string]any) (string, error) {
	parsed, err := texttemplate.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("i18n: parse message %q: %w", name, err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, parameters); err != nil {
		return "", fmt.Errorf("i18n: format message %q: %w", name, err)
	}
	return output.String(), nil
}

func pluralForm(locale Locale, count int64) Form {
	language := locale.Language()
	absolute := count
	if absolute < 0 {
		absolute = -absolute
	}
	mod10, mod100 := absolute%10, absolute%100
	switch language {
	case "ru", "uk", "be":
		if mod10 == 1 && mod100 != 11 {
			return One
		}
		if mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14) {
			return Few
		}
		if mod10 == 0 || mod10 >= 5 || (mod100 >= 11 && mod100 <= 14) {
			return Many
		}
	case "ar":
		if absolute == 0 {
			return Zero
		}
		if absolute == 1 {
			return One
		}
		if absolute == 2 {
			return Two
		}
		if mod100 >= 3 && mod100 <= 10 {
			return Few
		}
		if mod100 >= 11 && mod100 <= 99 {
			return Many
		}
	case "fr", "pt":
		if absolute == 0 || absolute == 1 {
			return One
		}
	default:
		if absolute == 1 {
			return One
		}
	}
	return Other
}

// TemplateFuncs exposes locale-explicit helpers for html/template. Returned
// strings remain untrusted and are escaped by html/template.
func (translator *Translator) TemplateFuncs() template.FuncMap {
	return template.FuncMap{"t": func(locale, category, key string, parameters map[string]any) (string, error) {
		parsed, err := ParseLocale(locale)
		if err != nil {
			return "", err
		}
		return translator.Translate(parsed, category, key, parameters)
	}, "tn": func(locale, category, key string, count int64, parameters map[string]any) (string, error) {
		parsed, err := ParseLocale(locale)
		if err != nil {
			return "", err
		}
		return translator.Plural(parsed, category, key, count, parameters)
	}}
}
