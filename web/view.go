package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"path"
	"reflect"
	"strings"
)

const templateExtension = ".html"

// Renderer загружает и конкурентно выполняет HTML-шаблоны из fs.FS.
type Renderer struct {
	templates     map[string]*template.Template
	defaultLayout string
}

// LayoutData передаёт layout уже экранированное содержимое view и исходные данные.
type LayoutData struct {
	Content template.HTML
	Data    any
}

type rendererConfig struct {
	defaultLayout string
	funcs         template.FuncMap
}

// RendererOption настраивает Renderer до разбора шаблонов.
type RendererOption func(*rendererConfig) error

// WithDefaultLayout задаёт layout, применяемый ко всем view по умолчанию.
func WithDefaultLayout(name string) RendererOption {
	return func(config *rendererConfig) error {
		normalized, err := normalizeTemplateName(name)
		if err != nil {
			return fmt.Errorf("web: default layout: %w", err)
		}
		config.defaultLayout = normalized
		return nil
	}
}

// WithTemplateFuncs добавляет функции, доступные во views, layouts и widgets.
// Имена widget и vueState зарезервированы фреймворком.
func WithTemplateFuncs(functions template.FuncMap) RendererOption {
	return func(config *rendererConfig) error {
		for name, function := range functions {
			if name == "widget" || name == "vueState" {
				return fmt.Errorf("web: функция шаблона %q зарезервирована", name)
			}
			config.funcs[name] = function
		}
		return nil
	}
}

type renderConfig struct {
	layout *string
}

// RenderOption настраивает отдельный рендеринг view.
type RenderOption func(*renderConfig) error

// WithLayout переопределяет layout для отдельного view.
func WithLayout(name string) RenderOption {
	return func(config *renderConfig) error {
		normalized, err := normalizeTemplateName(name)
		if err != nil {
			return fmt.Errorf("web: layout: %w", err)
		}
		config.layout = &normalized
		return nil
	}
}

// WithoutLayout отключает layout для отдельного view.
func WithoutLayout() RenderOption {
	return func(config *renderConfig) error {
		empty := ""
		config.layout = &empty
		return nil
	}
}

// NewRenderer загружает все .html-файлы из filesystem как независимые шаблоны.
// Логическое имя шаблона совпадает с путём файла без расширения.
func NewRenderer(filesystem fs.FS, options ...RendererOption) (*Renderer, error) {
	if filesystem == nil {
		return nil, errors.New("web: filesystem шаблонов не может быть nil")
	}
	config := rendererConfig{funcs: make(template.FuncMap)}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("web: опция Renderer не может быть nil")
		}
		if err := option(&config); err != nil {
			return nil, err
		}
	}
	if err := validateTemplateFuncs(config.funcs); err != nil {
		return nil, err
	}

	renderer := &Renderer{
		templates:     make(map[string]*template.Template),
		defaultLayout: config.defaultLayout,
	}
	functions := make(template.FuncMap, len(config.funcs)+2)
	for name, function := range config.funcs {
		functions[name] = function
	}
	functions["widget"] = renderer.Widget
	functions["vueState"] = VueState

	err := fs.WalkDir(filesystem, ".", func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filePath) != templateExtension {
			return nil
		}
		name := strings.TrimSuffix(filePath, templateExtension)
		source, err := fs.ReadFile(filesystem, filePath)
		if err != nil {
			return fmt.Errorf("web: чтение шаблона %q: %w", filePath, err)
		}
		parsed, err := template.New(name).Funcs(functions).Parse(string(source))
		if err != nil {
			return fmt.Errorf("web: разбор шаблона %q: %w", filePath, err)
		}
		renderer.templates[name] = parsed
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(renderer.templates) == 0 {
		return nil, errors.New("web: filesystem не содержит .html-шаблонов")
	}
	if renderer.defaultLayout != "" {
		if _, exists := renderer.templates[renderer.defaultLayout]; !exists {
			return nil, fmt.Errorf("web: default layout %q не найден", renderer.defaultLayout)
		}
	}
	return renderer, nil
}

// Render выполняет view и при необходимости оборачивает результат в layout.
// Результат полностью формируется до записи HTTP-ответа.
func (renderer *Renderer) Render(name string, data any, options ...RenderOption) ([]byte, error) {
	if renderer == nil {
		return nil, errors.New("web: Renderer не задан")
	}
	normalized, err := normalizeTemplateName(name)
	if err != nil {
		return nil, fmt.Errorf("web: view: %w", err)
	}
	config := renderConfig{}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("web: опция Render не может быть nil")
		}
		if err := option(&config); err != nil {
			return nil, err
		}
	}

	content, err := renderer.execute(normalized, data)
	if err != nil {
		return nil, fmt.Errorf("web: render view %q: %w", normalized, err)
	}
	layout := renderer.defaultLayout
	if config.layout != nil {
		layout = *config.layout
	}
	if layout == "" {
		return content, nil
	}
	result, err := renderer.execute(layout, LayoutData{Content: template.HTML(content), Data: data})
	if err != nil {
		return nil, fmt.Errorf("web: render layout %q: %w", layout, err)
	}
	return result, nil
}

// Widget выполняет переиспользуемый шаблон и возвращает безопасный HTML-фрагмент.
func (renderer *Renderer) Widget(name string, data any) (template.HTML, error) {
	if renderer == nil {
		return "", errors.New("web: Renderer не задан")
	}
	normalized, err := normalizeTemplateName(name)
	if err != nil {
		return "", fmt.Errorf("web: widget: %w", err)
	}
	content, err := renderer.execute(normalized, data)
	if err != nil {
		return "", fmt.Errorf("web: render widget %q: %w", normalized, err)
	}
	return template.HTML(content), nil
}

// VueState формирует безопасный application/json script для гидратации Vue.
// encoding/json экранирует <, > и &, поэтому данные не могут закрыть script-тег.
func VueState(id string, value any) (template.HTML, error) {
	if strings.TrimSpace(id) == "" {
		return "", errors.New("web: id Vue state не может быть пустым")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("web: сериализация Vue state: %w", err)
	}
	result := `<script type="application/json" id="` + html.EscapeString(id) + `">` + string(payload) + `</script>`
	return template.HTML(result), nil
}

// Render записывает view через Renderer как атомарный HTML-ответ.
func (c *Context) Render(status int, renderer *Renderer, name string, data any, options ...RenderOption) error {
	payload, err := renderer.Render(name, data, options...)
	if err != nil {
		return err
	}
	return c.HTML(status, string(payload))
}

func (renderer *Renderer) execute(name string, data any) ([]byte, error) {
	tmpl := renderer.templates[name]
	if tmpl == nil {
		return nil, fmt.Errorf("шаблон %q не найден", name)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func normalizeTemplateName(name string) (string, error) {
	if name == "" || strings.TrimSpace(name) != name || strings.HasSuffix(name, templateExtension) ||
		!fs.ValidPath(name) || path.Clean(name) != name {
		return "", fmt.Errorf("некорректное имя шаблона %q", name)
	}
	return name, nil
}

func validateTemplateFuncs(functions template.FuncMap) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("web: некорректные функции шаблона: %v", recovered)
		}
	}()
	for name, function := range functions {
		value := reflect.ValueOf(function)
		if !value.IsValid() || value.Kind() != reflect.Func {
			return fmt.Errorf("web: функция шаблона %q должна быть функцией", name)
		}
	}
	template.New("validation").Funcs(functions)
	return nil
}
