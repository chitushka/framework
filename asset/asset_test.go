package asset_test

import (
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/chitushka/framework/asset"
)

func TestRegistryResolvesDependenciesAndDeduplicates(t *testing.T) {
	registry := asset.NewRegistry()
	if err := registry.Register("vendor", asset.Bundle{
		Styles:  []asset.Resource{{URL: "/assets/vendor.css"}},
		Scripts: []asset.Resource{{URL: "/assets/vendor.js", Attributes: map[string]string{"defer": "defer"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("application", asset.Bundle{
		Dependencies: []string{"vendor"},
		Styles: []asset.Resource{
			{URL: "/assets/vendor.css"},
			{URL: `/assets/app.css?theme="dark"`},
		},
		ModulePreloads: []asset.Resource{{URL: "/assets/chunk.js", Attributes: map[string]string{"crossorigin": "anonymous"}}},
		Scripts:        []asset.Resource{{URL: "/assets/app.js", Attributes: map[string]string{"type": "module"}}},
	}); err != nil {
		t.Fatal(err)
	}

	head, err := registry.Head("application")
	if err != nil {
		t.Fatal(err)
	}
	body, err := registry.Body("application")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(head), "vendor.css") != 1 ||
		strings.Index(string(head), "vendor.css") > strings.Index(string(head), "app.css") ||
		!strings.Contains(string(head), `href="/assets/app.css?theme=%22dark%22"`) ||
		!strings.Contains(string(head), `rel="modulepreload"`) {
		t.Fatalf("unexpected head tags: %s", head)
	}
	if strings.Index(string(body), "vendor.js") > strings.Index(string(body), "app.js") ||
		!strings.Contains(string(body), `type="module"`) {
		t.Fatalf("unexpected body tags: %s", body)
	}
}

func TestRegistryTemplateFuncsAndEscaping(t *testing.T) {
	registry := asset.NewRegistry()
	if err := registry.Register("app", asset.Bundle{Scripts: []asset.Resource{{
		URL:        "javascript:alert(1)",
		Attributes: map[string]string{"nonce": `abc"><script>alert(1)</script>`},
	}}}); err != nil {
		t.Fatal(err)
	}
	page, err := template.New("page").Funcs(registry.TemplateFuncs("app")).Parse(`{{assetHead}}{{assetBody}}`)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := page.Execute(&output, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `src="#ZgotmplZ"`) || strings.Count(output.String(), "<script") != 1 {
		t.Fatalf("unsafe output: %s", output.String())
	}
}

func TestRegistryValidationAndResolutionErrors(t *testing.T) {
	registry := asset.NewRegistry()
	tests := []struct {
		name   string
		bundle asset.Bundle
	}{
		{"", asset.Bundle{}},
		{"blank dependency", asset.Bundle{Dependencies: []string{" "}}},
		{"empty resource", asset.Bundle{Styles: []asset.Resource{{}}}},
		{"reserved attribute", asset.Bundle{Scripts: []asset.Resource{{URL: "/app.js", Attributes: map[string]string{"src": "override"}}}}},
		{"invalid attribute", asset.Bundle{Scripts: []asset.Resource{{URL: "/app.js", Attributes: map[string]string{"bad name": "x"}}}}},
	}
	for _, test := range tests {
		if err := registry.Register(test.name, test.bundle); err == nil {
			t.Fatalf("%q: expected validation error", test.name)
		}
	}
	if err := registry.Register("app", asset.Bundle{Dependencies: []string{"missing"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Head("app"); err == nil {
		t.Fatal("expected missing dependency error")
	}
	if _, err := registry.Body("missing"); err == nil {
		t.Fatal("expected missing bundle error")
	}
	if err := registry.Register("app", asset.Bundle{}); err == nil {
		t.Fatal("expected duplicate registration error")
	}

	cycles := asset.NewRegistry()
	_ = cycles.Register("one", asset.Bundle{Dependencies: []string{"two"}})
	_ = cycles.Register("two", asset.Bundle{Dependencies: []string{"one"}})
	if _, err := cycles.Head("one"); err == nil {
		t.Fatal("expected dependency cycle error")
	}
	var nilRegistry *asset.Registry
	if err := nilRegistry.Register("app", asset.Bundle{}); err == nil {
		t.Fatal("expected nil registry error")
	}
	if _, err := nilRegistry.Head("app"); err == nil {
		t.Fatal("expected nil registry render error")
	}
}

func TestViteManifestProductionBundle(t *testing.T) {
	filesystem := fstest.MapFS{"manifest.json": {Data: []byte(`{
		"src/main.js":{"file":"assets/app-a1.js","css":["assets/app-b2.css"],"imports":["_vendor.js"]},
		"_vendor.js":{"file":"assets/vendor-c3.js","css":["assets/vendor-d4.css"]}
	}`)}}
	manifest, err := asset.LoadViteManifest(filesystem, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := manifest.Bundle("src/main.js", "/static/")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Styles) != 2 || bundle.Styles[0].URL != "/static/assets/vendor-d4.css" ||
		len(bundle.ModulePreloads) != 1 || bundle.ModulePreloads[0].URL != "/static/assets/vendor-c3.js" ||
		len(bundle.Scripts) != 1 || bundle.Scripts[0].URL != "/static/assets/app-a1.js" {
		t.Fatalf("unexpected Vite bundle: %#v", bundle)
	}
	if bundle.Scripts[0].Attributes["type"] != "module" {
		t.Fatalf("entry is not a module: %#v", bundle.Scripts[0])
	}
	if bundle.ModulePreloads[0].Attributes != nil {
		t.Fatalf("module preload has invalid script attributes: %#v", bundle.ModulePreloads[0])
	}
	cdnBundle, err := manifest.Bundle("src/main.js", "//cdn.example.com/static/")
	if err != nil || cdnBundle.Scripts[0].URL != "//cdn.example.com/static/assets/app-a1.js" {
		t.Fatalf("unexpected CDN bundle: %#v, %v", cdnBundle, err)
	}
}

func TestViteDevelopmentBundle(t *testing.T) {
	bundle, err := asset.ViteDevBundle("http://localhost:5173/", "/src/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Scripts) != 2 || bundle.Scripts[0].URL != "http://localhost:5173/@vite/client" ||
		bundle.Scripts[1].URL != "http://localhost:5173/src/main.js" {
		t.Fatalf("unexpected development bundle: %#v", bundle)
	}
}

func TestViteManifestErrors(t *testing.T) {
	if _, err := asset.LoadViteManifest(nil, "manifest.json"); err == nil {
		t.Fatal("expected nil filesystem error")
	}
	for name, filesystem := range map[string]fstest.MapFS{
		"missing": {},
		"invalid": {"manifest.json": {Data: []byte(`{`)}},
		"empty":   {"manifest.json": {Data: []byte(`{}`)}},
	} {
		if _, err := asset.LoadViteManifest(filesystem, "manifest.json"); err == nil {
			t.Fatalf("%s: expected manifest error", name)
		}
	}
	manifest := asset.ViteManifest{
		"main":    {File: "main.js", Imports: []string{"missing"}},
		"empty":   {},
		"cycle-a": {File: "a.js", Imports: []string{"cycle-b"}},
		"cycle-b": {File: "b.js", Imports: []string{"cycle-a"}},
	}
	for _, test := range []struct{ entry, base string }{
		{"missing", "/"}, {"main", "/"}, {"empty", "/"}, {"cycle-a", "/"}, {"main", "javascript:bad"},
	} {
		if _, err := manifest.Bundle(test.entry, test.base); err == nil {
			t.Fatalf("%s/%s: expected bundle error", test.entry, test.base)
		}
	}
	for _, server := range []string{"", "ftp://localhost", "http://", "http://localhost/?x=1"} {
		if _, err := asset.ViteDevBundle(server, "src/main.js"); err == nil {
			t.Fatalf("%q: expected dev server error", server)
		}
	}
	if _, err := asset.ViteDevBundle("http://localhost:5173", " "); err == nil {
		t.Fatal("expected empty entry error")
	}
}
