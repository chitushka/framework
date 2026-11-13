// Command checkdocs checks repository links and source-backed Markdown snippets.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/chitushka/framework/internal/doccheck"
)

func main() {
	root := flag.String("root", ".", "repository root")
	run := flag.Bool("run", false, "test onboarding in a fresh external module (prefetch dependencies first)")
	flag.Parse()
	report, err := doccheck.Check(*root)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("docs: %d documents, %d links, %d source fences OK\n", report.Documents, report.Links, report.Snippets)
	if *run {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := doccheck.Run(ctx, *root, os.Stdout); err != nil {
			log.Fatal(err)
		}
	}
}
