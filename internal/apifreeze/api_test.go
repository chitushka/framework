package apifreeze

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

func sample(t *testing.T, source string) Snapshot {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", "package fixture\n"+source, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("example.com/fixture", fset, []*ast.File{file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Schema: 1, Packages: map[string]map[string]string{pkg.Path(): Describe(pkg)}}
}

func TestPrivateImplementationAndPublicCompatibilityBoundaries(t *testing.T) {
	before := `type hidden struct{}; func (hidden) Promoted() int {return 0}; type Public struct { hidden; Name string ` + "`json:\"name\"`" + `; private int }; func (*Public) Pointer() {}; func Function[T ~int](value T) T {return value}; const Limit=4; type Alias = Public; type Interface interface { Method(string) error }`
	baseline := sample(t, before)
	privateChange := strings.Replace(before, "private int", "internalCache map[string]int", 1)
	if diff := Diff(baseline, sample(t, privateChange)); len(diff) != 0 {
		t.Fatalf("private state frozen: %v", diff)
	}
	for _, pair := range [][2]string{{"Name string", "Name int"}, {"json:\"name\"", "json:\"renamed\""}, {"Limit=4", "Limit=5"}, {"~int", "~int64"}, {"Method(string)", "Method(int)"}, {"func (*Public) Pointer", "func (Public) Pointer"}, {"Promoted() int", "Promoted() int64"}, {"type Alias = Public", "type Alias Public"}, {"Name string", "Label string"}} {
		if diff := Diff(baseline, sample(t, strings.Replace(before, pair[0], pair[1], 1))); len(diff) == 0 {
			t.Fatalf("missed public change: %v", pair)
		}
	}
	if diff := Diff(baseline, sample(t, before+"; func NewAPI() {}")); len(diff) == 0 {
		t.Fatal("unreviewed addition accepted")
	}
	if len(Diff(Snapshot{}, baseline)) == 0 {
		t.Fatal("empty baseline accepted")
	}
}

func TestRemovedAndAddedPackagesAndDeclarations(t *testing.T) {
	before := sample(t, "func Existing() {}")
	after := sample(t, "func Replacement() {}")
	if diff := strings.Join(Diff(before, after), "\n"); !strings.Contains(diff, "removed") || !strings.Contains(diff, "added") {
		t.Fatal(diff)
	}
	after.Packages = map[string]map[string]string{"example.com/new": {"func New": "func()"}}
	if len(Diff(before, after)) != 2 {
		t.Fatal("package movement not detected")
	}
}
