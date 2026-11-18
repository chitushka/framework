// Command apifreeze checks an exact, reviewed inventory of compiled public API.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chitushka/framework/internal/apifreeze"
)

func main() {
	root := flag.String("root", ".", "repository root")
	baseline := flag.String("baseline", "release/api-v1.json", "reviewed API snapshot")
	write := flag.Bool("write", false, "replace baseline explicitly after API review")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	api, err := apifreeze.Capture(ctx, *root)
	if err != nil {
		log.Fatal(err)
	}
	path := *baseline
	if !filepath.IsAbs(path) {
		path = filepath.Join(*root, path)
	}
	if *write {
		data, err := json.MarshalIndent(api, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			log.Fatal(err)
		}
	} else {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		var expected apifreeze.Snapshot
		if err := json.Unmarshal(data, &expected); err != nil {
			log.Fatal(err)
		}
		if differences := apifreeze.Diff(expected, api); len(differences) != 0 {
			log.Fatal(strings.Join(differences, "\n"))
		}
	}
	symbols := 0
	for _, items := range api.Packages {
		symbols += len(items)
	}
	fmt.Printf("API freeze: %d packages, %d declarations/selectors verified\n", len(api.Packages), symbols)
}
