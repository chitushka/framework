package gii

import (
	"strings"
	"testing"
)

func TestGeneratedVueRouteCannotTerminateScript(t *testing.T) {
	options := CRUDOptions{ModelPackage: "example.com/app/model", OutputDir: t.TempDir(), Route: "/</script><script>alert(1)</script>"}
	files, err := GenerateCRUD(sampleSchema(), options)
	if err != nil {
		t.Fatal(err)
	}
	vue := string(files[2].Content)
	if strings.Count(vue, "</script>") != 1 || !strings.Contains(vue, `\u003c/script\u003e`) {
		t.Fatalf("route escaped SFC script: %s", vue)
	}
	for _, route := range []string{"//attacker.example", "/safe?redirect=evil", "/safe#fragment", "/../outside", "/%2e%2e/outside", "/safe//path", "/bad%5Cpath", "/bad%00"} {
		options.Route = route
		if _, err := GenerateCRUD(sampleSchema(), options); err == nil {
			t.Fatalf("unsafe route accepted: %q", route)
		}
	}
}
