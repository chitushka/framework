// Package apifreeze records the compiled public Go API without freezing private
// implementation fields. It deliberately requires review for additions too.
package apifreeze

import (
	"context"
	"encoding/json"
	"fmt"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const Module = "github.com/chitushka/framework"

type Snapshot struct {
	Schema   int                          `json:"schema"`
	Packages map[string]map[string]string `json:"packages"`
}

type listedPackage struct {
	ImportPath string
	Name       string
	Export     string
	GoFiles    []string
	Module     *struct{ Path string }
}

func Capture(ctx context.Context, root string) (Snapshot, error) {
	exports := map[string]string{}
	var paths []string
	for _, directory := range []string{root, filepath.Join(root, "adapters")} {
		cmd := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-export", "-deps", "-json", "./...")
		cmd.Dir = directory
		output, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return Snapshot{}, fmt.Errorf("API go list %s: %w: %s", directory, err, strings.TrimSpace(string(exit.Stderr)))
			}
			return Snapshot{}, fmt.Errorf("API go list %s: %w", directory, err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(output)))
		for {
			var p listedPackage
			if err := decoder.Decode(&p); err == io.EOF {
				break
			} else if err != nil {
				return Snapshot{}, err
			}
			if p.Export != "" {
				exports[p.ImportPath] = p.Export
			}
			if p.Module == nil || (p.Module.Path != Module && p.Module.Path != Module+"/adapters") || p.Name == "main" || len(p.GoFiles) == 0 {
				continue
			}
			if strings.Contains(p.ImportPath, "/internal/") || strings.HasPrefix(p.ImportPath, Module+"/examples/") || strings.HasPrefix(p.ImportPath, Module+"/tools/") {
				continue
			}
			paths = append(paths, p.ImportPath)
		}
	}
	compiled := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("missing export data for %s", path)
		}
		return os.Open(file)
	})
	result := Snapshot{Schema: 1, Packages: map[string]map[string]string{}}
	sort.Strings(paths)
	for _, path := range paths {
		if _, seen := result.Packages[path]; seen {
			continue
		}
		pkg, err := compiled.Import(path)
		if err != nil {
			return Snapshot{}, fmt.Errorf("API import %s: %w", path, err)
		}
		result.Packages[path] = Describe(pkg)
	}
	if len(result.Packages) == 0 {
		return Snapshot{}, fmt.Errorf("no public packages found")
	}
	return result, nil
}

func Describe(pkg *types.Package) map[string]string {
	api := map[string]string{}
	qualifier := func(p *types.Package) string { return p.Path() }
	typeString := func(t types.Type) string { return types.TypeString(t, qualifier) }
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types.Const:
			api["const "+name] = typeString(obj.Type()) + " = " + obj.Val().ExactString()
		case *types.Var:
			api["var "+name] = typeString(obj.Type())
		case *types.Func:
			api["func "+name] = typeString(obj.Type())
		case *types.TypeName:
			if obj.IsAlias() {
				api["type "+name] = "alias " + typeString(types.Unalias(obj.Type()))
				continue
			}
			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			parameters := ""
			if params := named.TypeParams(); params != nil {
				for i := 0; i < params.Len(); i++ {
					parameters += params.At(i).Obj().Name() + " " + typeString(params.At(i).Constraint()) + ";"
				}
			}
			if structure, ok := named.Underlying().(*types.Struct); ok {
				var fields []string
				hidden := false
				for i := 0; i < structure.NumFields(); i++ {
					field := structure.Field(i)
					if !field.Exported() {
						hidden = true
						continue
					}
					fields = append(fields, field.Name()+" "+typeString(field.Type())+" tag="+strconv.Quote(structure.Tag(i))+" embedded="+strconv.FormatBool(field.Embedded()))
				}
				api["type "+name] = "[" + parameters + "] struct hidden=" + strconv.FormatBool(hidden) + " {" + strings.Join(fields, "; ") + "}"
			} else {
				api["type "+name] = "[" + parameters + "] " + typeString(named.Underlying())
			}
			for _, receiver := range []struct {
				name string
				typ  types.Type
			}{{name, named}, {"*" + name, types.NewPointer(named)}} {
				methods := types.NewMethodSet(receiver.typ)
				for i := 0; i < methods.Len(); i++ {
					method := methods.At(i)
					if method.Obj().Exported() {
						api["method "+receiver.name+"."+method.Obj().Name()] = typeString(method.Type())
					}
				}
			}
		}
	}
	return api
}

func Diff(expected, actual Snapshot) []string {
	var differences []string
	if expected.Schema != 1 || actual.Schema != 1 || len(expected.Packages) == 0 {
		return []string{"invalid/empty API baseline"}
	}
	for path, symbols := range expected.Packages {
		current, exists := actual.Packages[path]
		if !exists {
			differences = append(differences, "removed package "+path)
			continue
		}
		for name, signature := range symbols {
			value, exists := current[name]
			if !exists {
				differences = append(differences, "removed "+path+" "+name)
			} else if value != signature {
				differences = append(differences, "changed "+path+" "+name+"\n  want "+signature+"\n  got  "+value)
			}
		}
		for name := range current {
			if _, exists := symbols[name]; !exists {
				differences = append(differences, "added "+path+" "+name)
			}
		}
	}
	for path := range actual.Packages {
		if _, exists := expected.Packages[path]; !exists {
			differences = append(differences, "added package "+path)
		}
	}
	sort.Strings(differences)
	return differences
}
