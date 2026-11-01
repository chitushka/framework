package web_test

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/chitushka/framework/web"
)

type pageData struct {
	Title   string
	Message string
	State   map[string]string
}

func viewFilesystem() fstest.MapFS {
	return fstest.MapFS{
		"layouts/main.html":  {Data: []byte(`<!doctype html><title>{{.Data.Title}}</title><main>{{.Content}}</main>`)},
		"layouts/plain.html": {Data: []byte(`<article>{{.Content}}</article>`)},
		"views/home.html": {Data: []byte(
			`<h1>{{.Title}}</h1>{{widget "widgets/alert" .Message}}{{vueState "app-state" .State}}`,
		)},
		"views/broken.html":  {Data: []byte(`{{.Missing}}`)},
		"widgets/alert.html": {Data: []byte(`<aside>{{.}}</aside>`)},
	}
}

func TestRendererLayoutsWidgetsAndSafeVueState(t *testing.T) {
	renderer, err := web.NewRenderer(viewFilesystem(), web.WithDefaultLayout("layouts/main"))
	if err != nil {
		t.Fatal(err)
	}
	data := pageData{
		Title:   `Chitu & friends`,
		Message: `<strong>unsafe</strong>`,
		State:   map[string]string{"payload": `</script><script>alert("xss")</script>`},
	}
	payload, err := renderer.Render("views/home", data)
	if err != nil {
		t.Fatal(err)
	}
	output := string(payload)
	for _, expected := range []string{
		`<title>Chitu &amp; friends</title>`,
		`<h1>Chitu &amp; friends</h1>`,
		`<aside>&lt;strong&gt;unsafe&lt;/strong&gt;</aside>`,
		`\u003c/script\u003e\u003cscript\u003e`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("rendered output does not contain %q: %s", expected, output)
		}
	}
	if strings.Count(output, "</script>") != 1 {
		t.Fatalf("Vue state escaped from its script element: %s", output)
	}
}

func TestRendererLayoutOverridesAndTemplateFuncs(t *testing.T) {
	filesystem := viewFilesystem()
	filesystem["views/custom.html"] = &fstest.MapFile{Data: []byte(`{{upper .}}`)}
	renderer, err := web.NewRenderer(
		filesystem,
		web.WithDefaultLayout("layouts/main"),
		web.WithTemplateFuncs(template.FuncMap{"upper": strings.ToUpper}),
	)
	if err != nil {
		t.Fatal(err)
	}

	withoutLayout, err := renderer.Render("views/custom", "chitu", web.WithoutLayout())
	if err != nil || string(withoutLayout) != "CHITU" {
		t.Fatalf("unexpected view without layout: %q, %v", withoutLayout, err)
	}
	withLayout, err := renderer.Render("views/custom", "chitu", web.WithLayout("layouts/plain"))
	if err != nil || string(withLayout) != "<article>CHITU</article>" {
		t.Fatalf("unexpected overridden layout: %q, %v", withLayout, err)
	}
	widget, err := renderer.Widget("widgets/alert", `<b>message</b>`)
	if err != nil || string(widget) != `<aside>&lt;b&gt;message&lt;/b&gt;</aside>` {
		t.Fatalf("unexpected widget: %q, %v", widget, err)
	}
}

func TestContextRenderIsAtomic(t *testing.T) {
	renderer, err := web.NewRenderer(viewFilesystem(), web.WithDefaultLayout("layouts/main"))
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	context := &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	if err := context.Render(http.StatusOK, renderer, "views/home", pageData{Title: "Home"}); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
		!strings.Contains(response.Body.String(), "<title>Home</title>") {
		t.Fatalf("unexpected rendered response: %d %s %s", response.Code, response.Header(), response.Body.String())
	}

	response = httptest.NewRecorder()
	context = &web.Context{Writer: response, Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	if err := context.Render(http.StatusOK, renderer, "views/broken", struct{}{}, web.WithoutLayout()); err == nil {
		t.Fatal("expected template execution error")
	}
	if context.Written() {
		t.Fatal("template error committed HTTP response")
	}
	if err := context.HTML(http.StatusInternalServerError, "fallback"); err != nil {
		t.Fatal(err)
	}
}

func TestVueStateRoundTripAndEscaping(t *testing.T) {
	state := map[string]string{"html": `</script>`, "ampersand": "A&B"}
	result, err := web.VueState(`state" onload="alert(1)`, state)
	if err != nil {
		t.Fatal(err)
	}
	output := string(result)
	if strings.Contains(output, `" onload="`) || strings.Count(output, "</script>") != 1 {
		t.Fatalf("unsafe Vue state tag: %s", output)
	}
	start := strings.Index(output, ">") + 1
	end := strings.LastIndex(output, "</script>")
	var decoded map[string]string
	if err := json.Unmarshal([]byte(output[start:end]), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["html"] != state["html"] || decoded["ampersand"] != state["ampersand"] {
		t.Fatalf("state did not round-trip: %#v", decoded)
	}
}

func TestRendererSupportsConcurrentExecution(t *testing.T) {
	renderer, err := web.NewRenderer(viewFilesystem(), web.WithDefaultLayout("layouts/main"))
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsChannel := make(chan error, 20)
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, renderErr := renderer.Render("views/home", pageData{Title: "Concurrent"})
			errorsChannel <- renderErr
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRendererValidation(t *testing.T) {
	valid := fstest.MapFS{"view.html": {Data: []byte(`ok`)}}
	invalidTemplate := fstest.MapFS{"view.html": {Data: []byte(`{{`)}}
	withoutTemplates := fstest.MapFS{"readme.txt": {Data: []byte(`none`)}}

	tests := map[string]func() error{
		"nil filesystem": func() error { _, err := web.NewRenderer(nil); return err },
		"nil option":     func() error { _, err := web.NewRenderer(valid, nil); return err },
		"invalid parse":  func() error { _, err := web.NewRenderer(invalidTemplate); return err },
		"no templates":   func() error { _, err := web.NewRenderer(withoutTemplates); return err },
		"missing layout": func() error {
			_, err := web.NewRenderer(valid, web.WithDefaultLayout("layouts/missing"))
			return err
		},
		"reserved function": func() error {
			_, err := web.NewRenderer(valid, web.WithTemplateFuncs(template.FuncMap{"vueState": strings.ToUpper}))
			return err
		},
		"not a function": func() error {
			_, err := web.NewRenderer(valid, web.WithTemplateFuncs(template.FuncMap{"invalid": 42}))
			return err
		},
		"invalid function name": func() error {
			_, err := web.NewRenderer(valid, web.WithTemplateFuncs(template.FuncMap{"bad-name": strings.ToUpper}))
			return err
		},
	}
	for name, test := range tests {
		if err := test(); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}

	renderer, err := web.NewRenderer(valid)
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]func() error{
		"missing view": func() error { _, err := renderer.Render("missing", nil); return err },
		"invalid view": func() error { _, err := renderer.Render("../view", nil); return err },
		"nil render option": func() error {
			_, err := renderer.Render("view", nil, nil)
			return err
		},
		"missing render layout": func() error {
			_, err := renderer.Render("view", nil, web.WithLayout("missing"))
			return err
		},
		"missing widget": func() error { _, err := renderer.Widget("missing", nil); return err },
		"empty state id": func() error { _, err := web.VueState(" ", nil); return err },
		"invalid state":  func() error { _, err := web.VueState("state", make(chan int)); return err },
	} {
		if err := test(); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}
