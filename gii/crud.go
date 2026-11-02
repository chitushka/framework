// Package gii contains deterministic code generators used by the console toolset.
package gii

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/chitushka/framework/db/ar"
)

type CRUDOptions struct {
	Package      string
	ModelPackage string
	ModelAlias   string
	ModelType    string
	OutputDir    string
	VueOutput    string
	Route        string
}
type GeneratedFile struct {
	Path    string
	Content []byte
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// GenerateCRUD creates a JSON CRUD controller, an allowlisted search model and
// a Vue 3/AdminKit-compatible component from one inspected table schema.
func GenerateCRUD(schema ar.TableSchema, options CRUDOptions) ([]GeneratedFile, error) {
	if len(schema.Columns) == 0 {
		return nil, errors.New("gii: schema has no columns")
	}
	table := lastPart(schema.Name)
	if !identifier.MatchString(table) {
		return nil, fmt.Errorf("gii: invalid table %q", schema.Name)
	}
	if options.Package == "" {
		options.Package = "controller"
	}
	if !identifier.MatchString(options.Package) {
		return nil, fmt.Errorf("gii: invalid package %q", options.Package)
	}
	if options.ModelPackage == "" {
		return nil, errors.New("gii: model package import path is required")
	}
	if options.ModelAlias == "" {
		options.ModelAlias = filepath.Base(options.ModelPackage)
	}
	if !identifier.MatchString(options.ModelAlias) {
		return nil, errors.New("gii: invalid model package alias")
	}
	if options.ModelType == "" {
		options.ModelType = exported(singular(table))
	}
	if !goName(options.ModelType, true) {
		return nil, fmt.Errorf("gii: invalid model type %q", options.ModelType)
	}
	if strings.TrimSpace(options.OutputDir) == "" {
		return nil, errors.New("gii: output directory is required")
	}
	if options.Route == "" {
		options.Route = "/" + strings.ReplaceAll(table, "_", "-")
	}
	if !strings.HasPrefix(options.Route, "/") {
		return nil, errors.New("gii: route must start with /")
	}
	primary := make([]ar.ColumnSchema, 0)
	writable := make([]ar.ColumnSchema, 0)
	for _, column := range schema.Columns {
		if !identifier.MatchString(column.Name) {
			return nil, fmt.Errorf("gii: invalid column %q", column.Name)
		}
		if column.PrimaryKey {
			primary = append(primary, column)
		}
		if !column.PrimaryKey && !column.AutoIncrement && !column.Version {
			writable = append(writable, column)
		}
	}
	if len(primary) == 0 {
		return nil, errors.New("gii: CRUD requires a primary key")
	}
	base := snake(singular(table))
	controller, err := generateController(schema, options, primary, writable)
	if err != nil {
		return nil, err
	}
	search, err := generateSearch(schema, options)
	if err != nil {
		return nil, err
	}
	vue := generateVue(schema, options, primary, writable)
	vuePath := options.VueOutput
	if vuePath == "" {
		vuePath = filepath.Join(options.OutputDir, base+".vue")
	}
	return []GeneratedFile{{Path: filepath.Join(options.OutputDir, base+"_controller.go"), Content: controller}, {Path: filepath.Join(options.OutputDir, base+"_search.go"), Content: search}, {Path: vuePath, Content: vue}}, nil
}

func WriteCRUD(schema ar.TableSchema, options CRUDOptions) ([]string, error) {
	files, err := GenerateCRUD(schema, options)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if _, err := os.Stat(file.Path); err == nil {
			return nil, fmt.Errorf("gii: file already exists: %s", file.Path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	created := make([]string, 0, len(files))
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
			rollback(created)
			return nil, fmt.Errorf("gii: create directory: %w", err)
		}
		handle, err := os.OpenFile(file.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			rollback(created)
			return nil, fmt.Errorf("gii: create %s: %w", file.Path, err)
		}
		_, writeErr := handle.Write(file.Content)
		closeErr := handle.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(file.Path)
			rollback(created)
			return nil, errors.Join(writeErr, closeErr)
		}
		created = append(created, file.Path)
	}
	return created, nil
}
func rollback(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

func generateController(schema ar.TableSchema, o CRUDOptions, primary, writable []ar.ColumnSchema) ([]byte, error) {
	var source bytes.Buffer
	typeName, alias := o.ModelType, o.ModelAlias
	fmt.Fprintf(&source, "package %s\n\n", o.Package)
	imports := []string{"errors", "net/http", "github.com/chitushka/framework/db/ar", "github.com/chitushka/framework/di", "github.com/chitushka/framework/web", o.ModelPackage}
	needsStrconv := false
	needsTime := false
	for _, column := range primary {
		if parseKind(column) != "string" {
			needsStrconv = true
		}
	}
	for _, column := range writable {
		if strings.Contains(columnGoType(column), "time.Time") {
			needsTime = true
		}
	}
	if needsStrconv {
		imports = append(imports, "strconv")
	}
	if needsTime {
		imports = append(imports, "time")
	}
	sort.Strings(imports)
	source.WriteString("import (\n")
	for _, path := range imports {
		if path == o.ModelPackage {
			fmt.Fprintf(&source, "\t%s %q\n", alias, path)
		} else {
			fmt.Fprintf(&source, "\t%q\n", path)
		}
	}
	source.WriteString(")\n\n")
	fmt.Fprintf(&source, "type %sController struct { web.BaseController }\n\n", typeName)
	fmt.Fprintf(&source, "type %sInput struct {\n", typeName)
	for _, column := range writable {
		fmt.Fprintf(&source, "\t%s *%s `json:%q`\n", exported(column.Name), columnGoType(column), column.Name)
	}
	source.WriteString("}\n\n")
	fmt.Fprintf(&source, "func (input %sInput) Apply(model *%s.%s) {\n", typeName, alias, typeName)
	for _, column := range writable {
		field := exported(column.Name)
		fmt.Fprintf(&source, "\tif input.%s != nil { model.%s = *input.%s }\n", field, field, field)
	}
	source.WriteString("}\n\n")
	fmt.Fprintf(&source, "func (*%sController) Index(ctx *web.Context, repository *ar.Repository[%s.%s]) error {\n\tsearch := New%sSearch(ctx.Request.URL.Query())\n\tmodels, err := search.Apply(repository.Find()).Limit(100).All(ctx.Request.Context())\n\tif err != nil { return err }\n\treturn ctx.JSON(http.StatusOK, models)\n}\n\n", typeName, alias, typeName, typeName)
	fmt.Fprintf(&source, "func (*%sController) Show(ctx *web.Context, repository *ar.Repository[%s.%s]) error {\n\tmodel, err := find%s(ctx, repository)\n\tif err != nil { return err }\n\treturn ctx.JSON(http.StatusOK, model)\n}\n\n", typeName, alias, typeName, typeName)
	fmt.Fprintf(&source, "func (*%sController) Create(ctx *web.Context, repository *ar.Repository[%s.%s]) error {\n\tvar input %sInput\n\tif err := ctx.BindJSON(&input); err != nil { return err }\n\tmodel := new(%s.%s)\n\tinput.Apply(model)\n\tif err := repository.Insert(ctx.Request.Context(), model); err != nil { return err }\n\treturn ctx.JSON(http.StatusCreated, model)\n}\n\n", typeName, alias, typeName, typeName, alias, typeName)
	fmt.Fprintf(&source, "func (*%sController) Update(ctx *web.Context, repository *ar.Repository[%s.%s]) error {\n\tmodel, err := find%s(ctx, repository)\n\tif err != nil { return err }\n\tvar input %sInput\n\tif err := ctx.BindJSON(&input); err != nil { return err }\n\tinput.Apply(model)\n\tif err := repository.Update(ctx.Request.Context(), model); err != nil { return err }\n\treturn ctx.JSON(http.StatusOK, model)\n}\n\n", typeName, alias, typeName, typeName, typeName)
	fmt.Fprintf(&source, "func (*%sController) Delete(ctx *web.Context, repository *ar.Repository[%s.%s]) error {\n\tmodel, err := find%s(ctx, repository)\n\tif err != nil { return err }\n\tif err := repository.Delete(ctx.Request.Context(), model); err != nil { return err }\n\treturn ctx.NoContent(http.StatusNoContent)\n}\n\n", typeName, alias, typeName, typeName)
	fmt.Fprintf(&source, "func find%s(ctx *web.Context, repository *ar.Repository[%s.%s]) (*%s.%s, error) {\n", typeName, alias, typeName, alias, typeName)
	args := make([]string, 0, len(primary))
	for _, column := range primary {
		name := snake(column.Name)
		expression, lines := parseParameter(column, name)
		source.WriteString(lines)
		args = append(args, expression)
	}
	fmt.Fprintf(&source, "\tmodel, err := repository.FindByPK(ctx.Request.Context(), %s)\n\tif errors.Is(err, ar.ErrNotFound) { return nil, web.NewHTTPError(http.StatusNotFound, %q, err) }\n\treturn model, err\n}\n\n", strings.Join(args, ", "), singular(lastPart(schema.Name))+" not found")
	detailRoute := o.Route
	for _, column := range primary {
		detailRoute += "/{" + column.Name + "}"
	}
	fmt.Fprintf(&source, "func Register%sRoutes(router *web.Router, container *di.Container) error {\n", typeName)
	fmt.Fprintf(&source, "\tif err := router.GETAction(%q, container, (*%sController).Index); err != nil { return err }\n", o.Route, typeName)
	fmt.Fprintf(&source, "\tif err := router.POSTAction(%q, container, (*%sController).Create); err != nil { return err }\n", o.Route, typeName)
	fmt.Fprintf(&source, "\tif err := router.GETAction(%q, container, (*%sController).Show); err != nil { return err }\n", detailRoute, typeName)
	fmt.Fprintf(&source, "\tif err := router.HandleAction(http.MethodPut, %q, container, (*%sController).Update); err != nil { return err }\n", detailRoute, typeName)
	fmt.Fprintf(&source, "\treturn router.HandleAction(http.MethodDelete, %q, container, (*%sController).Delete)\n}\n", detailRoute, typeName)
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gii: format controller: %w\n%s", err, source.String())
	}
	return formatted, nil
}

func generateSearch(schema ar.TableSchema, o CRUDOptions) ([]byte, error) {
	var source bytes.Buffer
	fmt.Fprintf(&source, "package %s\n\n", o.Package)
	fmt.Fprintf(&source, "import (\n\t\"net/url\"\n\n\tframeworkdb \"github.com/chitushka/framework/db\"\n\t\"github.com/chitushka/framework/db/ar\"\n\t%s %q\n)\n\n", o.ModelAlias, o.ModelPackage)
	fmt.Fprintf(&source, "type %sSearch struct { values map[string]string }\n\n", o.ModelType)
	fmt.Fprintf(&source, "func New%sSearch(query url.Values) %sSearch {\n\tvalues := make(map[string]string)\n", o.ModelType, o.ModelType)
	for _, column := range schema.Columns {
		fmt.Fprintf(&source, "\tif items, found := query[%q]; found && len(items) > 0 { values[%q] = items[0] }\n", column.Name, column.Name)
	}
	fmt.Fprintf(&source, "\treturn %sSearch{values: values}\n}\n\n", o.ModelType)
	fmt.Fprintf(&source, "func (search %sSearch) Apply(finder *ar.Finder[%s.%s]) *ar.Finder[%s.%s] {\n", o.ModelType, o.ModelAlias, o.ModelType, o.ModelAlias, o.ModelType)
	for _, column := range schema.Columns {
		fmt.Fprintf(&source, "\tif value, found := search.values[%q]; found { finder = finder.AndWhere(frameworkdb.Eq(%q, value)) }\n", column.Name, column.Name)
	}
	source.WriteString("\treturn finder\n}\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gii: format search model: %w", err)
	}
	return formatted, nil
}

func generateVue(schema ar.TableSchema, o CRUDOptions, primary, writable []ar.ColumnSchema) []byte {
	var source bytes.Buffer
	source.WriteString("<script setup>\nimport { onMounted, reactive, ref } from 'vue'\n\n")
	fmt.Fprintf(&source, "const endpoint = %q\n", o.Route)
	source.WriteString("const items = ref([])\nconst editing = ref(false)\nconst form = reactive({")
	for index, column := range writable {
		if index > 0 {
			source.WriteString(", ")
		}
		fmt.Fprintf(&source, "%s: %s", column.Name, vueDefault(column))
	}
	source.WriteString("})\n\nasync function load() { items.value = await (await fetch(endpoint)).json() }\nfunction edit(item = null) { editing.value = item || true; Object.assign(form, item || {")
	for index, column := range writable {
		if index > 0 {
			source.WriteString(", ")
		}
		fmt.Fprintf(&source, "%s: %s", column.Name, vueDefault(column))
	}
	source.WriteString("}) }\n")
	source.WriteString("function itemURL(item) { return endpoint + '")
	for _, column := range primary {
		source.WriteString("/' + encodeURIComponent(item." + column.Name + ")")
	}
	source.WriteString(" }\n")
	source.WriteString("async function save() { const creating = editing.value === true; const target = creating ? endpoint : itemURL(editing.value); await fetch(target, { method: creating ? 'POST' : 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(form) }); editing.value = false; await load() }\nasync function remove(item) { await fetch(itemURL(item), {method: 'DELETE'}); await load() }\nonMounted(load)\n</script>\n\n<template>\n  <div class=\"card\">\n    <div class=\"card-header d-flex justify-content-between\"><h5 class=\"card-title mb-0\">" + o.ModelType + "</h5><button class=\"btn btn-primary\" @click=\"edit()\">Create</button></div>\n    <div class=\"card-body\">\n      <form v-if=\"editing\" class=\"row g-3 mb-4\" @submit.prevent=\"save\">\n")
	for _, column := range writable {
		binding, inputType := "v-model", "text"
		switch parseKind(column) {
		case "int", "uint", "float":
			binding, inputType = "v-model.number", "number"
		}
		if strings.TrimPrefix(columnGoType(column), "*") == "bool" {
			inputType = "checkbox"
		}
		if inputType == "checkbox" {
			fmt.Fprintf(&source, "        <div class=\"col-md-6 form-check\"><input v-model=\"form.%s\" class=\"form-check-input\" type=\"checkbox\"><label class=\"form-check-label\">%s</label></div>\n", column.Name, column.Name)
		} else {
			fmt.Fprintf(&source, "        <div class=\"col-md-6\"><label class=\"form-label\">%s</label><input %s=\"form.%s\" class=\"form-control\" type=\"%s\"></div>\n", column.Name, binding, column.Name, inputType)
		}
	}
	source.WriteString("        <div class=\"col-12\"><button class=\"btn btn-primary\" type=\"submit\">Save</button></div>\n      </form>\n      <div class=\"table-responsive\"><table class=\"table table-hover\"><thead><tr>")
	for _, column := range schema.Columns {
		fmt.Fprintf(&source, "<th>%s</th>", column.Name)
	}
	source.WriteString("<th></th></tr></thead><tbody><tr v-for=\"item in items\" :key=\"itemURL(item)\">")
	for _, column := range schema.Columns {
		fmt.Fprintf(&source, "<td>{{ item.%s }}</td>", column.Name)
	}
	source.WriteString("<td class=\"text-end\"><button class=\"btn btn-sm btn-outline-primary me-2\" @click=\"edit(item)\">Edit</button><button class=\"btn btn-sm btn-outline-danger\" @click=\"remove(item)\">Delete</button></td></tr></tbody></table></div>\n    </div>\n  </div>\n</template>\n")
	return source.Bytes()
}

func parseParameter(column ar.ColumnSchema, name string) (string, string) {
	kind := parseKind(column)
	fieldType := columnGoType(column)
	switch kind {
	case "int":
		return name, fmt.Sprintf("\t%sValue, err := strconv.ParseInt(ctx.Param(%q), 10, 64)\n\tif err != nil { return nil, web.NewHTTPError(http.StatusBadRequest, %q, err) }\n\t%s := %s(%sValue)\n", name, column.Name, "invalid "+column.Name, name, strings.TrimPrefix(fieldType, "*"), name)
	case "uint":
		return name, fmt.Sprintf("\t%sValue, err := strconv.ParseUint(ctx.Param(%q), 10, 64)\n\tif err != nil { return nil, web.NewHTTPError(http.StatusBadRequest, %q, err) }\n\t%s := %s(%sValue)\n", name, column.Name, "invalid "+column.Name, name, strings.TrimPrefix(fieldType, "*"), name)
	default:
		return name, fmt.Sprintf("\t%s := ctx.Param(%q)\n", name, column.Name)
	}
}
func parseKind(column ar.ColumnSchema) string {
	value := strings.TrimPrefix(columnGoType(column), "*")
	if strings.HasPrefix(value, "int") {
		return "int"
	}
	if strings.HasPrefix(value, "uint") {
		return "uint"
	}
	if strings.HasPrefix(value, "float") {
		return "float"
	}
	return "string"
}
func vueDefault(column ar.ColumnSchema) string {
	value := strings.TrimPrefix(columnGoType(column), "*")
	if value == "bool" {
		return "false"
	}
	if strings.HasPrefix(value, "int") || strings.HasPrefix(value, "uint") || strings.HasPrefix(value, "float") {
		return "0"
	}
	return "''"
}
func columnGoType(column ar.ColumnSchema) string {
	value := column.GoType
	if value == "" {
		typeName := strings.ToLower(column.DataType)
		switch {
		case strings.Contains(typeName, "bool"):
			value = "bool"
		case strings.Contains(typeName, "int"), strings.Contains(typeName, "serial"):
			value = "int64"
		case strings.Contains(typeName, "float"), strings.Contains(typeName, "decimal"), strings.Contains(typeName, "numeric"):
			value = "float64"
		case strings.Contains(typeName, "date"), strings.Contains(typeName, "time"):
			value = "time.Time"
		case strings.Contains(typeName, "blob"), strings.Contains(typeName, "binary"), strings.Contains(typeName, "bytea"):
			value = "[]byte"
		default:
			value = "string"
		}
	}
	if column.Nullable && value != "[]byte" && !strings.HasPrefix(value, "*") {
		value = "*" + value
	}
	return value
}
func exported(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '_' || r == '-' || unicode.IsSpace(r) })
	var result strings.Builder
	for _, part := range parts {
		lower := strings.ToLower(part)
		if lower == "id" {
			result.WriteString("ID")
			continue
		}
		runes := []rune(part)
		result.WriteRune(unicode.ToUpper(runes[0]))
		result.WriteString(string(runes[1:]))
	}
	return result.String()
}
func snake(value string) string {
	var result strings.Builder
	for index, r := range value {
		if unicode.IsUpper(r) && index > 0 {
			result.WriteByte('_')
		}
		result.WriteRune(unicode.ToLower(r))
	}
	return result.String()
}
func singular(value string) string {
	if strings.HasSuffix(value, "ies") {
		return strings.TrimSuffix(value, "ies") + "y"
	}
	if strings.HasSuffix(value, "s") && !strings.HasSuffix(value, "ss") {
		return strings.TrimSuffix(value, "s")
	}
	return value
}
func lastPart(value string) string { parts := strings.Split(value, "."); return parts[len(parts)-1] }
func goName(value string, exportedName bool) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if r == '_' || unicode.IsLetter(r) || (index > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return !exportedName || unicode.IsUpper([]rune(value)[0])
}
