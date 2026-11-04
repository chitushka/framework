package asset_test

import (
	"testing"

	"github.com/chitushka/framework/asset"
)

func TestAssetEventAttributesAreRejected(t *testing.T) {
	for _, name := range []string{"onload", "OnError", "ONCLICK", "srcdoc", "src", "href", "bad\"attribute"} {
		registry := asset.NewRegistry()
		if err := registry.Register("app", asset.Bundle{Scripts: []asset.Resource{{URL: "/app.js", Attributes: map[string]string{name: "alert(document.cookie)"}}}}); err == nil {
			t.Fatalf("unsafe attribute accepted: %s", name)
		}
	}
}
