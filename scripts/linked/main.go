// Command linked writes the Go of the Ergo packages the ergo binary links
// (internal/linked): for each row of linked.Packages, the command lines it
// generates and its lowered Go package. Run it from the repository root
// after an edit of a linked module, of the std packages it imports, or of
// the compiler; TestLinkedGoIsFresh fails until it has run.
//
//	go run ./scripts/linked
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/linked"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "linked:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("run it from the repository root: %w", err)
	}
	for _, p := range linked.Packages {
		files, err := linked.Generate(root, p)
		if err != nil {
			return err
		}
		for _, rel := range linked.Sorted(files) {
			full := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, files[rel], 0o644); err != nil {
				return err
			}
			fmt.Println(rel)
		}
	}
	return nil
}
