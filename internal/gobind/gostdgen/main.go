// Command gostdgen writes the bindings of Go's standard library into
// internal/sketch/std/gostd/src, for go generate (interop.md, Go's standard
// library). It replaces the folder's contents.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/gobind"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gostdgen DIR")
		os.Exit(2)
	}
	root := os.Args[1]
	files, err := gobind.StdBindings(".", os.Environ())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gostdgen:", err)
		os.Exit(1)
	}
	if err := os.RemoveAll(filepath.Clean(root)); err != nil {
		fmt.Fprintln(os.Stderr, "gostdgen:", err)
		os.Exit(1)
	}
	if err := gobind.WriteAll(root, files); err != nil {
		fmt.Fprintln(os.Stderr, "gostdgen:", err)
		os.Exit(1)
	}
	fmt.Printf("gostdgen: %d bindings\n", len(files))
}
