// Command stdcheck checks the embedded standard library: the prelude and
// every std package, with the checker the copy builds. The mutant runner
// builds it in a mutated copy for a row of a std .ergo file (compiles.go):
// a row whose std no longer checks is invalid, never killed. It prints the
// first diagnostic of each package that does not check and exits 1.
package main

import (
	"fmt"
	"os"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
)

func main() {
	bad := false
	if ds := check.PreludeDiagnostics(); len(ds) > 0 {
		fmt.Printf("std prelude has diagnostics: %s\n", ds[0])
		bad = true
	}
	for _, path := range std.Packages() {
		if _, err := check.Std.Import(path); err != nil {
			fmt.Println(err)
			bad = true
		}
	}
	if bad {
		os.Exit(1)
	}
}
