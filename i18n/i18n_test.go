package i18n_test

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/chitushka/framework/i18n"
	"github.com/chitushka/framework/web"
)

func testTranslator(t *testing.T) *i18n.Translator {
	t.Helper()
	filesystem := fstest.MapFS{
		"en/app.json": {Data: []byte(`{"welcome":"Hello, {{.name}}!","items":{"one":"{{.count}} item","other":"{{.count}} items"},"unsafe":"{{.value}}"}`)},
		"ru/app.json": {Data: []byte(`{"welcome":"Привет, {{.name}}!","items":{"one":"{{.count}} предмет","few":"{{.count}} предмета","many":"{{.count}} предметов","other":"{{.count}} предмета"}}`)},
		"fr/app.json": {Data: []byte(`{"welcome":"Bonjour, {{.name}} !"}`)},
	}
	catalog, err := i18n.LoadFS(filesystem)
	if err != nil {
		t.Fatal(err)
	}
	translator, err := i18n.New(catalog, i18n.Config{DefaultLocale: "en", SupportedLocales: []string{"en", "ru", "fr"}, Fallbacks: map[string][]string{"fr-CA": {"fr"}}})
	if err != nil {
		t.Fatal(err)
	}
	return translator
}

func TestLocaleParsingMatchingAndAcceptLanguage(t *testing.T) {
	locale, err := i18n.ParseLocale("zh_hant_tw")
	if err != nil || locale != "zh-Hant-TW" {
		t.Fatalf("locale = %q, %v", locale, err)
	}
	parents := locale.Parents()
	if len(parents) != 2 || parents[0] != "zh-Hant" || parents[1] != "zh" {
		t.Fatalf("parents = %#v", parents)
	}
	preferences := i18n.ParseAcceptLanguage("de;q=0.4, ru-RU;q=0.9, en;q=0.8, broken_tag;q=x")
	if len(preferences) != 3 || preferences[0] != "ru-RU" || preferences[1] != "en" {
		t.Fatalf("preferences = %#v", preferences)
	}
	matcher, err := i18n.NewMatcher([]string{"en", "ru", "zh-Hant"}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if got := matcher.Match(i18n.MustLocale("zh-Hant-TW")); got != "zh-Hant" {
		t.Fatalf("match = %q", got)
	}
	if got := matcher.Match(i18n.MustLocale("de")); got != "en" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestTranslationFallbackFormattingAndPlurals(t *testing.T) {
	translator := testTranslator(t)
	ctx, err := i18n.WithLocale(context.Background(), "ru-RU")
	if err != nil {
		t.Fatal(err)
	}
	welcome, err := translator.T(ctx, "app", "welcome", map[string]any{"name": "Ада"})
	if err != nil || welcome != "Привет, Ада!" {
		t.Fatalf("welcome = %q, %v", welcome, err)
	}
	cases := map[int64]string{1: "1 предмет", 2: "2 предмета", 5: "5 предметов", 11: "11 предметов", 21: "21 предмет"}
	for count, want := range cases {
		got, err := translator.N(ctx, "app", "items", count, nil)
		if err != nil || got != want {
			t.Fatalf("N(%d) = %q, %v", count, got, err)
		}
	}
	missing, err := translator.T(ctx, "app", "missing", nil)
	if err != nil || missing != "missing" {
		t.Fatalf("missing = %q, %v", missing, err)
	}
	if _, err := translator.T(ctx, "app", "welcome", nil); err == nil {
		t.Fatal("missing template parameter accepted")
	}
}

func TestTemplateFuncsKeepTranslatedValuesEscaped(t *testing.T) {
	translator := testTranslator(t)
	parsed, err := template.New("view").Funcs(translator.TemplateFuncs()).Parse(`{{t "en" "app" "unsafe" .}}`)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := parsed.Execute(&output, map[string]any{"value": "<script>alert(1)</script>"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "<script>") || !strings.Contains(output.String(), "&lt;script&gt;") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestPluralFamiliesAndExplicitFallback(t *testing.T) {
	catalog := i18n.NewMemoryCatalog()
	forms := map[i18n.Form]string{i18n.Zero: "zero", i18n.One: "one", i18n.Two: "two", i18n.Few: "few", i18n.Many: "many", i18n.Other: "other"}
	for _, locale := range []string{"ar", "en", "fr"} {
		if err := catalog.Add(i18n.MustLocale(locale), "app", map[string]i18n.Message{"count": i18n.Plural(forms)}); err != nil {
			t.Fatal(err)
		}
	}
	translator, err := i18n.New(catalog, i18n.Config{DefaultLocale: "en", SupportedLocales: []string{"en", "fr", "ar"}, Fallbacks: map[string][]string{"fr-CA": {"fr"}}})
	if err != nil {
		t.Fatal(err)
	}
	arabic := map[int64]string{0: "zero", 1: "one", 2: "two", 3: "few", 11: "many", 100: "other"}
	for count, want := range arabic {
		got, err := translator.Plural(i18n.MustLocale("ar"), "app", "count", count, nil)
		if err != nil || got != want {
			t.Fatalf("Arabic %d = %q, %v", count, got, err)
		}
	}
	if got, _ := translator.Plural(i18n.MustLocale("en"), "app", "count", 2, nil); got != "other" {
		t.Fatalf("English plural = %q", got)
	}
	if got, _ := translator.Plural(i18n.MustLocale("fr-CA"), "app", "count", 0, nil); got != "one" {
		t.Fatalf("French fallback plural = %q", got)
	}
	if got := translator.Match(i18n.MustLocale("ar-EG")); got != "ar" {
		t.Fatalf("translator match = %q", got)
	}
}

func TestMiddlewareSelectsAndPersistsLocale(t *testing.T) {
	translator := testTranslator(t)
	router := web.NewRouter()
	_ = router.Use(i18n.Middleware(translator, i18n.MiddlewareConfig{}))
	_ = router.GET("/", func(ctx *web.Context) error {
		locale, _ := i18n.LocaleFromContext(ctx.Request.Context())
		return ctx.JSON(200, map[string]string{"locale": locale.String()})
	})
	query := httptest.NewRequest(http.MethodGet, "/?lang=ru-RU", nil)
	query.Header.Set("Accept-Language", "fr")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, query)
	if response.Code != 200 || response.Body.String() != `{"locale":"ru"}`+"\n" {
		t.Fatalf("query response = %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Language") != "ru" || len(response.Result().Cookies()) != 1 {
		t.Fatalf("headers=%#v cookies=%#v", response.Header(), response.Result().Cookies())
	}
	cookieRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	cookieRequest.AddCookie(response.Result().Cookies()[0])
	cookieResponse := httptest.NewRecorder()
	router.ServeHTTP(cookieResponse, cookieRequest)
	if cookieResponse.Header().Get("Content-Language") != "ru" {
		t.Fatalf("cookie locale = %q", cookieResponse.Header().Get("Content-Language"))
	}
	headerRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	headerRequest.Header.Set("Accept-Language", "de;q=.5, fr-CA;q=.9")
	headerResponse := httptest.NewRecorder()
	router.ServeHTTP(headerResponse, headerRequest)
	if headerResponse.Header().Get("Content-Language") != "fr" {
		t.Fatalf("header locale = %q", headerResponse.Header().Get("Content-Language"))
	}
}

func TestCatalogAndConfigurationValidation(t *testing.T) {
	if _, err := i18n.ParseLocale(""); err == nil {
		t.Fatal("empty locale accepted")
	}
	if _, err := i18n.ParseLocale("e"); err == nil {
		t.Fatal("short language accepted")
	}
	if _, err := i18n.LoadFS(fstest.MapFS{}); err == nil {
		t.Fatal("empty catalog accepted")
	}
	catalog := i18n.NewMemoryCatalog()
	if err := catalog.Add(i18n.MustLocale("ru"), "app", map[string]i18n.Message{"ok": i18n.Text("Да")}); err != nil {
		t.Fatal(err)
	}
	if locales := catalog.Locales(); len(locales) != 1 || locales[0] != "ru" {
		t.Fatalf("locales = %#v", locales)
	}
	if _, _, err := catalog.Lookup(nil, i18n.MustLocale("ru"), "app", "ok"); err == nil {
		t.Fatal("nil lookup context accepted")
	}
	if err := catalog.Add(i18n.MustLocale("en"), "app", map[string]i18n.Message{"bad": i18n.Plural(map[i18n.Form]string{i18n.One: "one"})}); err == nil {
		t.Fatal("plural without other accepted")
	}
	if err := catalog.Add(i18n.MustLocale("en"), "app", map[string]i18n.Message{"bad": i18n.Plural(map[i18n.Form]string{i18n.Other: "x", i18n.Form("invalid"): "y"})}); err == nil {
		t.Fatal("unknown plural form accepted")
	}
	if _, err := i18n.New(catalog, i18n.Config{}); err == nil {
		t.Fatal("empty config accepted")
	}
	strict, err := i18n.New(catalog, i18n.Config{DefaultLocale: "en", SupportedLocales: []string{"en"}, ErrorOnMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Translate(i18n.MustLocale("en"), "app", "missing", nil); err == nil {
		t.Fatal("strict missing message accepted")
	}
	matcher, err := i18n.NewMatcher([]string{"en", "ru"}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if !matcher.Supported(i18n.MustLocale("ru")) || matcher.Supported(i18n.MustLocale("de")) {
		t.Fatal("supported locale mismatch")
	}
	if _, err := i18n.NewMatcher(nil, "en"); err == nil {
		t.Fatal("empty matcher accepted")
	}
	if _, err := i18n.WithLocale(nil, "en"); err == nil {
		t.Fatal("nil locale context accepted")
	}
	if _, err := i18n.LoadFS(fstest.MapFS{"nested/en/app.json": {Data: []byte(`{}`)}}); err == nil {
		t.Fatal("nested catalog path accepted")
	}
}
