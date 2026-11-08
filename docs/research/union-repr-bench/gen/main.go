// Command gen writes the generated sources of the union-repr bench module
// (internal/gensrc). Run it from the module root: go run ./gen.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"unionreprbench/internal/gensrc"
)

func main() {
	for name, src := range gensrc.Files() {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(name, src, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
}
