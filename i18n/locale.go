// Package i18n provides locale matching and translated message catalogs.
package i18n

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Locale is a canonicalized, comparable BCP 47 language tag.
type Locale string

func ParseLocale(value string) (Locale, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "_", "-"))
	if value == "" {
		return "", errors.New("i18n: locale is empty")
	}
	parts := strings.Split(value, "-")
	if len(parts[0]) < 2 || len(parts[0]) > 8 || !letters(parts[0]) {
		return "", fmt.Errorf("i18n: invalid language tag %q", value)
	}
	parts[0] = strings.ToLower(parts[0])
	for index := 1; index < len(parts); index++ {
		part := parts[index]
		if part == "" || len(part) > 8 || !alphaNumeric(part) {
			return "", fmt.Errorf("i18n: invalid language tag %q", value)
		}
		switch {
		case len(part) == 4 && letters(part):
			parts[index] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
		case (len(part) == 2 && letters(part)) || (len(part) == 3 && digits(part)):
			parts[index] = strings.ToUpper(part)
		default:
			parts[index] = strings.ToLower(part)
		}
	}
	return Locale(strings.Join(parts, "-")), nil
}

func MustLocale(value string) Locale {
	locale, err := ParseLocale(value)
	if err != nil {
		panic(err)
	}
	return locale
}
func (locale Locale) String() string { return string(locale) }
func (locale Locale) Language() string {
	value := string(locale)
	if index := strings.IndexByte(value, '-'); index >= 0 {
		return value[:index]
	}
	return value
}

// Parents returns progressively less specific locales, excluding the receiver.
func (locale Locale) Parents() []Locale {
	parts := strings.Split(string(locale), "-")
	result := make([]Locale, 0, len(parts)-1)
	for len(parts) > 1 {
		parts = parts[:len(parts)-1]
		result = append(result, Locale(strings.Join(parts, "-")))
	}
	return result
}

type languagePreference struct {
	locale   Locale
	quality  float64
	order    int
	wildcard bool
}

// ParseAcceptLanguage parses and sorts an HTTP Accept-Language value by quality.
// Malformed ranges and q=0 entries are ignored.
func ParseAcceptLanguage(header string) []Locale {
	var preferences []languagePreference
	for order, raw := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(raw), ";")
		tag := strings.TrimSpace(fields[0])
		quality := 1.0
		for _, parameter := range fields[1:] {
			key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
			if found && strings.EqualFold(key, "q") {
				parsed, err := strconv.ParseFloat(value, 64)
				if err != nil || parsed < 0 || parsed > 1 {
					quality = 0
				} else {
					quality = parsed
				}
			}
		}
		if quality <= 0 {
			continue
		}
		if tag == "*" {
			preferences = append(preferences, languagePreference{quality: quality, order: order, wildcard: true})
			continue
		}
		locale, err := ParseLocale(tag)
		if err == nil {
			preferences = append(preferences, languagePreference{locale: locale, quality: quality, order: order})
		}
	}
	sort.SliceStable(preferences, func(i, j int) bool { return preferences[i].quality > preferences[j].quality })
	result := make([]Locale, 0, len(preferences))
	for _, preference := range preferences {
		if !preference.wildcard {
			result = append(result, preference.locale)
		}
	}
	return result
}

// Matcher selects an explicitly supported locale with parent fallback.
type Matcher struct {
	supported []Locale
	index     map[Locale]struct{}
	fallback  Locale
}

func NewMatcher(supported []string, fallback string) (*Matcher, error) {
	if len(supported) == 0 {
		return nil, errors.New("i18n: supported locales are required")
	}
	matcher := &Matcher{index: make(map[Locale]struct{})}
	for _, value := range supported {
		locale, err := ParseLocale(value)
		if err != nil {
			return nil, err
		}
		if _, exists := matcher.index[locale]; !exists {
			matcher.index[locale] = struct{}{}
			matcher.supported = append(matcher.supported, locale)
		}
	}
	var err error
	matcher.fallback, err = ParseLocale(fallback)
	if err != nil {
		return nil, err
	}
	if _, exists := matcher.index[matcher.fallback]; !exists {
		return nil, errors.New("i18n: fallback locale is not supported")
	}
	return matcher, nil
}
func (matcher *Matcher) Match(preferred ...Locale) Locale {
	for _, candidate := range preferred {
		if _, ok := matcher.index[candidate]; ok {
			return candidate
		}
		for _, parent := range candidate.Parents() {
			if _, ok := matcher.index[parent]; ok {
				return parent
			}
		}
	}
	return matcher.fallback
}
func (matcher *Matcher) MatchFound(preferred ...Locale) (Locale, bool) {
	for _, candidate := range preferred {
		if _, ok := matcher.index[candidate]; ok {
			return candidate, true
		}
		for _, parent := range candidate.Parents() {
			if _, ok := matcher.index[parent]; ok {
				return parent, true
			}
		}
	}
	return "", false
}
func (matcher *Matcher) Fallback() Locale { return matcher.fallback }
func (matcher *Matcher) Supported(locale Locale) bool {
	_, found := matcher.index[locale]
	return found
}

func letters(value string) bool {
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}
func digits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
func alphaNumeric(value string) bool {
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
