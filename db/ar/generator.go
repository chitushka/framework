package ar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type ColumnSchema struct {
	Name          string
	DataType      string
	GoType        string
	Nullable      bool
	PrimaryKey    bool
	AutoIncrement bool
	Version       bool
}

type RelationSchema struct {
	Name          string
	ModelType     string
	LocalColumn   string
	ForeignColumn string
	Many          bool
}

type TableSchema struct {
	Name      string
	Columns   []ColumnSchema
	Relations []RelationSchema
}

// Inspector adapts database-specific schema discovery to the model generator.
type Inspector interface {
	Inspect(context.Context, string) (TableSchema, error)
}

type GenerateOptions struct {
	Package  string
	TypeName string
	Output   string
}

type Generator struct{ Inspector Inspector }

func (generator Generator) Generate(ctx context.Context, table string, options GenerateOptions) ([]byte, error) {
	if generator.Inspector == nil {
		return nil, errors.New("ar: schema inspector must not be nil")
	}
	schema, err := generator.Inspector.Inspect(ctx, table)
	if err != nil {
		return nil, fmt.Errorf("ar: inspect table %s: %w", table, err)
	}
	return GenerateModel(schema, options)
}

func (generator Generator) GenerateFile(ctx context.Context, table string, options GenerateOptions) error {
	if strings.TrimSpace(options.Output) == "" {
		return errors.New("ar: generator output path must not be empty")
	}
	source, err := generator.Generate(ctx, table, options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.Output), 0o755); err != nil {
		return fmt.Errorf("ar: create model directory: %w", err)
	}
	file, err := os.OpenFile(options.Output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("ar: create model file: %w", err)
	}
	if _, err := file.Write(source); err != nil {
		_ = file.Close()
		_ = os.Remove(options.Output)
		return fmt.Errorf("ar: write model file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(options.Output)
		return fmt.Errorf("ar: close model file: %w", err)
	}
	return nil
}

// GenerateModel creates a gofmt-formatted Active Record struct and typed relations.
func GenerateModel(schema TableSchema, options GenerateOptions) ([]byte, error) {
	if err := validateIdentifierPath(schema.Name); err != nil {
		return nil, fmt.Errorf("ar: schema table: %w", err)
	}
	if len(schema.Columns) == 0 {
		return nil, errors.New("ar: schema has no columns")
	}
	packageName := options.Package
	if packageName == "" {
		packageName = "models"
	}
	if !goIdentifier(packageName, false) {
		return nil, fmt.Errorf("ar: invalid package name %q", packageName)
	}
	typeName := options.TypeName
	if typeName == "" {
		typeName = exportedName(singular(lastIdentifier(schema.Name)))
	}
	if !goIdentifier(typeName, true) {
		return nil, fmt.Errorf("ar: invalid type name %q", typeName)
	}

	usesTime := false
	usesRelations := len(schema.Relations) > 0
	fields := make([]generatedField, len(schema.Columns))
	seenFields := make(map[string]struct{}, len(schema.Columns))
	for i, column := range schema.Columns {
		if column.Name == "" {
			return nil, errors.New("ar: column name must not be empty")
		}
		fieldName := exportedName(column.Name)
		if !goIdentifier(fieldName, true) {
			return nil, fmt.Errorf("ar: invalid generated field name %q", fieldName)
		}
		if _, found := seenFields[fieldName]; found {
			return nil, fmt.Errorf("ar: duplicate generated field %q", fieldName)
		}
		seenFields[fieldName] = struct{}{}
		goType := column.GoType
		if goType == "" {
			goType = databaseGoType(column.DataType)
		}
		if column.Nullable && goType != "[]byte" && !strings.HasPrefix(goType, "*") {
			goType = "*" + goType
		}
		usesTime = usesTime || strings.Contains(goType, "time.Time")
		options := make([]string, 0, 3)
		if column.PrimaryKey {
			options = append(options, "primary")
		}
		if column.AutoIncrement {
			options = append(options, "auto")
		}
		if column.Version {
			options = append(options, "version")
		}
		dbTag := column.Name
		if len(options) > 0 {
			dbTag += "," + strings.Join(options, ",")
		}
		fields[i] = generatedField{name: fieldName, type_: goType, dbTag: dbTag, jsonTag: column.Name}
	}

	var source bytes.Buffer
	fmt.Fprintf(&source, "package %s\n\n", packageName)
	imports := make([]string, 0, 2)
	if usesRelations {
		imports = append(imports, "context", "github.com/chitushka/framework/db/ar")
	}
	if usesTime {
		imports = append(imports, "time")
	}
	if len(imports) > 0 {
		sort.Strings(imports)
		source.WriteString("import (\n")
		for _, path := range imports {
			fmt.Fprintf(&source, "\t%q\n", path)
		}
		source.WriteString(")\n\n")
	}
	fmt.Fprintf(&source, "type %s struct {\n", typeName)
	for _, field := range fields {
		fmt.Fprintf(&source, "\t%s %s `db:%q json:%q`\n", field.name, field.type_, field.dbTag, field.jsonTag)
	}
	source.WriteString("}\n\n")
	fmt.Fprintf(&source, "func (%s) TableName() string { return %q }\n", typeName, schema.Name)
	for _, relation := range schema.Relations {
		if !goIdentifier(relation.Name, true) || !goIdentifier(relation.ModelType, true) {
			return nil, fmt.Errorf("ar: invalid relation %q or model type %q", relation.Name, relation.ModelType)
		}
		if relation.LocalColumn == "" || relation.ForeignColumn == "" {
			return nil, fmt.Errorf("ar: relation %s has empty columns", relation.Name)
		}
		fmt.Fprintf(&source, "\nfunc (model *%s) %s(ctx context.Context, repository *ar.Repository[%s]) ", typeName, relation.Name, relation.ModelType)
		if relation.Many {
			fmt.Fprintf(&source, "([]*%s, error) {\n", relation.ModelType)
			fmt.Fprintf(&source, "\treturn ar.HasMany(ctx, model, repository, ar.Relation{LocalColumn: %q, ForeignColumn: %q})\n", relation.LocalColumn, relation.ForeignColumn)
		} else {
			fmt.Fprintf(&source, "(*%s, error) {\n", relation.ModelType)
			fmt.Fprintf(&source, "\treturn ar.HasOne(ctx, model, repository, ar.Relation{LocalColumn: %q, ForeignColumn: %q})\n", relation.LocalColumn, relation.ForeignColumn)
		}
		source.WriteString("}\n")
	}
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("ar: format generated model: %w", err)
	}
	return formatted, nil
}

type generatedField struct {
	name    string
	type_   string
	dbTag   string
	jsonTag string
}

func databaseGoType(dataType string) string {
	type_ := strings.ToLower(strings.TrimSpace(dataType))
	switch {
	case strings.Contains(type_, "bool"), type_ == "bit":
		return "bool"
	case strings.Contains(type_, "int"), type_ == "serial", type_ == "bigserial":
		return "int64"
	case strings.Contains(type_, "decimal"), strings.Contains(type_, "numeric"), strings.Contains(type_, "real"), strings.Contains(type_, "float"), strings.Contains(type_, "double"):
		return "float64"
	case strings.Contains(type_, "date"), strings.Contains(type_, "time"):
		return "time.Time"
	case strings.Contains(type_, "binary"), strings.Contains(type_, "blob"), strings.Contains(type_, "bytea"):
		return "[]byte"
	default:
		return "string"
	}
}

func exportedName(value string) string {
	parts := strings.FieldsFunc(value, func(char rune) bool { return char == '_' || char == '-' || unicode.IsSpace(char) })
	var result strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if initialism := commonInitialism(strings.ToLower(part)); initialism != "" {
			result.WriteString(initialism)
			continue
		}
		runes := []rune(part)
		result.WriteRune(unicode.ToUpper(runes[0]))
		result.WriteString(string(runes[1:]))
	}
	return result.String()
}

func commonInitialism(value string) string {
	switch value {
	case "id":
		return "ID"
	case "api":
		return "API"
	case "http":
		return "HTTP"
	case "https":
		return "HTTPS"
	case "sql":
		return "SQL"
	case "url":
		return "URL"
	case "uuid":
		return "UUID"
	default:
		return ""
	}
}

func singular(value string) string {
	if strings.HasSuffix(value, "ies") && len(value) > 3 {
		return strings.TrimSuffix(value, "ies") + "y"
	}
	if strings.HasSuffix(value, "s") && !strings.HasSuffix(value, "ss") && !strings.HasSuffix(value, "us") && len(value) > 1 {
		return strings.TrimSuffix(value, "s")
	}
	return value
}

func lastIdentifier(value string) string {
	parts := strings.Split(value, ".")
	return parts[len(parts)-1]
}

func goIdentifier(value string, exported bool) bool {
	if value == "" {
		return false
	}
	for i, char := range value {
		if char == '_' || unicode.IsLetter(char) || i > 0 && unicode.IsDigit(char) {
			continue
		}
		return false
	}
	if exported && !unicode.IsUpper([]rune(value)[0]) {
		return false
	}
	return true
}
