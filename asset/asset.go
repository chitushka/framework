// Package asset связывает серверные шаблоны с CSS и JavaScript приложения.
package asset

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"regexp"
	"slices"
	"strings"
	"sync"
)

var attributeName = regexp.MustCompile(`^[A-Za-z_:][A-Za-z0-9_.:-]*$`)

// Resource описывает один внешний ресурс и его HTML-атрибуты.
// URL всегда задаётся отдельно и не может быть заменён через Attributes.
type Resource struct {
	URL        string
	Attributes map[string]string
}

// Bundle группирует ресурсы и зависимости от других bundles.
// Styles и ModulePreloads выводятся в head, Scripts — в конце body.
type Bundle struct {
	Dependencies   []string
	Styles         []Resource
	ModulePreloads []Resource
	Scripts        []Resource
}

// Registry хранит AssetBundle и безопасен для конкурентного использования.
type Registry struct {
	mu      sync.RWMutex
	bundles map[string]Bundle
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry {
	return &Registry{bundles: make(map[string]Bundle)}
}

// Register регистрирует bundle. Повторная регистрация имени запрещена.
func (registry *Registry) Register(name string, bundle Bundle) error {
	if registry == nil {
		return errors.New("asset: Registry не задан")
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateBundle(bundle); err != nil {
		return fmt.Errorf("asset: bundle %q: %w", name, err)
	}
	bundle = cloneBundle(bundle)
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.bundles[name]; exists {
		return fmt.Errorf("asset: bundle %q уже зарегистрирован", name)
	}
	registry.bundles[name] = bundle
	return nil
}

// Head рендерит stylesheets и modulepreload в порядке зависимостей.
func (registry *Registry) Head(names ...string) (template.HTML, error) {
	bundles, err := registry.resolve(names)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	seenStyles := make(map[string]struct{})
	seenPreloads := make(map[string]struct{})
	for _, bundle := range bundles {
		for _, resource := range bundle.Styles {
			if addResource(seenStyles, resource) {
				tag, renderErr := renderVoidTag("link", resource, map[string]string{"rel": "stylesheet"})
				if renderErr != nil {
					return "", renderErr
				}
				output.WriteString(tag)
			}
		}
		for _, resource := range bundle.ModulePreloads {
			if addResource(seenPreloads, resource) {
				tag, renderErr := renderVoidTag("link", resource, map[string]string{"rel": "modulepreload"})
				if renderErr != nil {
					return "", renderErr
				}
				output.WriteString(tag)
			}
		}
	}
	return template.HTML(output.String()), nil
}

// Body рендерит scripts в порядке зависимостей.
func (registry *Registry) Body(names ...string) (template.HTML, error) {
	bundles, err := registry.resolve(names)
	if err != nil {
		return "", err
	}
	var output strings.Builder
	seen := make(map[string]struct{})
	for _, bundle := range bundles {
		for _, resource := range bundle.Scripts {
			if !addResource(seen, resource) {
				continue
			}
			tag, renderErr := renderScript(resource)
			if renderErr != nil {
				return "", renderErr
			}
			output.WriteString(tag)
		}
	}
	return template.HTML(output.String()), nil
}

// TemplateFuncs привязывает указанные bundles к assetHead и assetBody.
func (registry *Registry) TemplateFuncs(names ...string) template.FuncMap {
	bound := slices.Clone(names)
	return template.FuncMap{
		"assetHead": func() (template.HTML, error) { return registry.Head(bound...) },
		"assetBody": func() (template.HTML, error) { return registry.Body(bound...) },
	}
}

func (registry *Registry) resolve(names []string) ([]Bundle, error) {
	if registry == nil {
		return nil, errors.New("asset: Registry не задан")
	}
	registry.mu.RLock()
	snapshot := make(map[string]Bundle, len(registry.bundles))
	for name, bundle := range registry.bundles {
		snapshot[name] = bundle
	}
	registry.mu.RUnlock()

	const (
		unvisited = iota
		visiting
		visited
	)
	states := make(map[string]int)
	stack := make([]string, 0, len(snapshot))
	resolved := make([]Bundle, 0, len(snapshot))
	var visit func(string) error
	visit = func(name string) error {
		bundle, exists := snapshot[name]
		if !exists {
			return fmt.Errorf("asset: bundle %q не зарегистрирован", name)
		}
		switch states[name] {
		case visiting:
			return fmt.Errorf("asset: циклическая зависимость: %s -> %s", strings.Join(stack, " -> "), name)
		case visited:
			return nil
		}
		states[name] = visiting
		stack = append(stack, name)
		for _, dependency := range bundle.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		states[name] = visited
		resolved = append(resolved, bundle)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func validateName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("asset: некорректное имя bundle %q", name)
	}
	return nil
}

func validateBundle(bundle Bundle) error {
	for _, dependency := range bundle.Dependencies {
		if err := validateName(dependency); err != nil {
			return fmt.Errorf("dependency: %w", err)
		}
	}
	for _, resources := range [][]Resource{bundle.Styles, bundle.ModulePreloads, bundle.Scripts} {
		for _, resource := range resources {
			if strings.TrimSpace(resource.URL) == "" {
				return errors.New("URL ресурса не может быть пустым")
			}
			for name := range resource.Attributes {
				lower := strings.ToLower(name)
				if !attributeName.MatchString(name) || strings.HasPrefix(lower, "on") || lower == "srcdoc" || strings.EqualFold(name, "href") || strings.EqualFold(name, "src") {
					return fmt.Errorf("некорректный атрибут %q", name)
				}
			}
		}
	}
	return nil
}

func cloneBundle(bundle Bundle) Bundle {
	bundle.Dependencies = slices.Clone(bundle.Dependencies)
	bundle.Styles = cloneResources(bundle.Styles)
	bundle.ModulePreloads = cloneResources(bundle.ModulePreloads)
	bundle.Scripts = cloneResources(bundle.Scripts)
	return bundle
}

func cloneResources(resources []Resource) []Resource {
	result := make([]Resource, len(resources))
	for index, resource := range resources {
		result[index] = Resource{URL: resource.URL}
		if resource.Attributes != nil {
			result[index].Attributes = make(map[string]string, len(resource.Attributes))
			for name, value := range resource.Attributes {
				result[index].Attributes[name] = value
			}
		}
	}
	return result
}

func addResource(seen map[string]struct{}, resource Resource) bool {
	key := resource.URL + "\x00" + attributesKey(resource.Attributes)
	if _, exists := seen[key]; exists {
		return false
	}
	seen[key] = struct{}{}
	return true
}

func attributesKey(attributes map[string]string) string {
	names := make([]string, 0, len(attributes))
	for name := range attributes {
		names = append(names, name)
	}
	slices.Sort(names)
	var key strings.Builder
	for _, name := range names {
		key.WriteString(name)
		key.WriteByte('=')
		key.WriteString(attributes[name])
		key.WriteByte(0)
	}
	return key.String()
}

func renderVoidTag(name string, resource Resource, fixed map[string]string) (string, error) {
	attributes := cloneAttributes(resource.Attributes)
	for key, value := range fixed {
		attributes[key] = value
	}
	attributes["href"] = resource.URL
	return renderTag(name, attributes, true)
}

func renderScript(resource Resource) (string, error) {
	attributes := cloneAttributes(resource.Attributes)
	attributes["src"] = resource.URL
	return renderTag("script", attributes, false)
}

func cloneAttributes(attributes map[string]string) map[string]string {
	result := make(map[string]string, len(attributes)+2)
	for name, value := range attributes {
		result[name] = value
	}
	return result
}

func renderTag(name string, attributes map[string]string, void bool) (string, error) {
	names := make([]string, 0, len(attributes))
	for attribute := range attributes {
		names = append(names, attribute)
	}
	slices.Sort(names)
	var source strings.Builder
	source.WriteByte('<')
	source.WriteString(name)
	values := make([]any, 0, len(names))
	for index, attribute := range names {
		source.WriteByte(' ')
		source.WriteString(attribute)
		source.WriteString(`="{{index . `)
		_, _ = fmt.Fprint(&source, index)
		source.WriteString(`}}"`)
		values = append(values, attributes[attribute])
	}
	if void {
		source.WriteByte('>')
	} else {
		source.WriteString("></")
		source.WriteString(name)
		source.WriteByte('>')
	}
	parsed, err := template.New("asset-tag").Parse(source.String())
	if err != nil {
		return "", fmt.Errorf("asset: разбор HTML-тега: %w", err)
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, values); err != nil {
		return "", fmt.Errorf("asset: рендер HTML-тега: %w", err)
	}
	return output.String(), nil
}
