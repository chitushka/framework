// Package performance provides internal, reproducible performance fixtures.
package performance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing/fstest"

	"github.com/chitushka/framework/db"
	"github.com/chitushka/framework/di"
	"github.com/chitushka/framework/web"
)

// Payload is shared by the HTTP and SQL comparisons.
type Payload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func Name() string { return strings.Repeat("x", 128) }

// Routing builds identical successful GET responses. The baseline uses Go's
// method/path mux; framework-specific safety work remains part of its overhead.
func Routing(variant string, routes int) (http.Handler, error) {
	if routes < 1 || (variant != "Chitu" && variant != "NetHTTP") {
		return nil, fmt.Errorf("performance: invalid routing fixture")
	}
	name := Name()
	if variant == "NetHTTP" {
		mux := http.NewServeMux()
		for i := range routes {
			mux.HandleFunc(fmt.Sprintf("GET /items%d/{id}", i), func(w http.ResponseWriter, r *http.Request) {
				WriteJSON(w, 200, Payload{r.PathValue("id"), name})
			})
		}
		return mux, nil
	}
	router := web.NewRouter()
	for i := range routes {
		if err := router.GET(fmt.Sprintf("/items%d/{id}", i), func(c *web.Context) error {
			return c.JSON(200, Payload{c.Param("id"), name})
		}); err != nil {
			return nil, err
		}
	}
	return router, nil
}

// WriteJSON deliberately uses the same marshaling/newline/content type as Context.JSON.
func WriteJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "encoding failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

type GraphConfig struct{ Label string }
type GraphRepository struct{ Config *GraphConfig }
type GraphService struct{ Repository *GraphRepository }

// NoInline keeps the manual factory allocations observable, as for a caller
// retaining the returned service, rather than optimizing the graph onto a stack.
//
//go:noinline
func newConfig() *GraphConfig { return &GraphConfig{Label: "catalog"} }

//go:noinline
func newRepository(c *GraphConfig) *GraphRepository { return &GraphRepository{Config: c} }

//go:noinline
func newService(r *GraphRepository) *GraphService { return &GraphService{Repository: r} }

type Resolve func() (*GraphService, error)

func DI(singleton bool) (Resolve, Resolve, error) {
	c := di.New()
	if err := c.Provide(newConfig); err != nil {
		return nil, nil, err
	}
	if err := c.Provide(newRepository); err != nil {
		return nil, nil, err
	}
	var options []di.Option
	if singleton {
		options = append(options, di.Singleton())
	}
	if err := c.Provide(newService, options...); err != nil {
		return nil, nil, err
	}
	manual := Resolve(func() (*GraphService, error) { return newService(newRepository(newConfig())), nil })
	if singleton {
		instance, _ := manual()
		manual = func() (*GraphService, error) { return instance, nil }
	}
	resolve := func() (*GraphService, error) {
		var instance *GraphService
		err := c.Resolve(&instance)
		return instance, err
	}
	if _, err := resolve(); err != nil { // warm singleton/reflection before timing
		return nil, nil, err
	}
	return manual, resolve, nil
}

const viewSource = `<section><h1>{{.Title}}</h1>{{range .Rows}}<p data-id="{{.ID}}">{{.Name}}</p>{{end}}</section>`
const layoutSource = `<!doctype html><title>{{.Data.Title}}</title><main>{{.Content}}</main>`

type renderData struct {
	Title string
	Rows  []Payload
}

type Render func() ([]byte, error)

func Rendering() (Render, Render, error) {
	data := renderData{Title: "Catalog <&>"}
	for i := range 20 {
		data.Rows = append(data.Rows, Payload{strconv.Itoa(i + 1), "<item &>" + Name()})
	}
	renderer, err := web.NewRenderer(fstest.MapFS{
		"catalog.html":      {Data: []byte(viewSource)},
		"layouts/main.html": {Data: []byte(layoutSource)},
	}, web.WithDefaultLayout("layouts/main"))
	if err != nil {
		return nil, nil, err
	}
	view, err := template.New("catalog").Parse(viewSource)
	if err != nil {
		return nil, nil, err
	}
	layout, err := template.New("layout").Parse(layoutSource)
	if err != nil {
		return nil, nil, err
	}
	manual := func() ([]byte, error) {
		var content, output bytes.Buffer
		if err := view.Execute(&content, data); err != nil {
			return nil, err
		}
		// Only the already escaped html/template result is trusted, as in Renderer.
		if err := layout.Execute(&output, web.LayoutData{Content: template.HTML(content.Bytes()), Data: data}); err != nil {
			return nil, err
		}
		return output.Bytes(), nil
	}
	framework := func() ([]byte, error) { return renderer.Render("catalog", data) }
	for _, render := range []Render{manual, framework} {
		if _, err := render(); err != nil { // html/template contextual escaping warm-up
			return nil, nil, err
		}
	}
	return manual, framework, nil
}

type BuiltQuery struct {
	SQL  string
	Args []any
}

//go:noinline
func staticQuery(statement string, id int, name string) BuiltQuery {
	return BuiltQuery{statement, []any{id, name}}
}

func SQLBuild(dialect string) (func() BuiltQuery, func() (BuiltQuery, error), error) {
	d, err := db.NewDefaultDialectRegistry().Require(dialect)
	if err != nil {
		return nil, nil, err
	}
	statement := `SELECT "id", "name" FROM "perf_items" WHERE ("id" = $1) AND ("name" = $2) LIMIT 20`
	if dialect == db.MySQL {
		statement = "SELECT `id`, `name` FROM `perf_items` WHERE (`id` = ?) AND (`name` = ?) LIMIT 20"
	} else if dialect != db.PostgreSQL {
		return nil, nil, fmt.Errorf("performance: unsupported SQL fixture")
	}
	name := Name()
	manual := func() BuiltQuery { return staticQuery(statement, 42, name) }
	framework := func() (BuiltQuery, error) {
		query, args, err := db.NewQueryBuilder(d).Select("id", "name").From("perf_items").
			Where(db.And(db.Eq("id", 42), db.Eq("name", name))).Limit(20).Build()
		return BuiltQuery{query, args}, err
	}
	return manual, framework, nil
}

// Operation adapters keep results live without a contended global benchmark sink.
func ResolveOperation(resolve Resolve) func() error {
	return func() error { value, err := resolve(); runtime.KeepAlive(value); return err }
}
func RenderOperation(render Render) func() error {
	return func() error { value, err := render(); runtime.KeepAlive(value); return err }
}
