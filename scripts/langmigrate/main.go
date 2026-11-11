// Command langmigrate rewrites Ergo sources for a language change of wave
// 2. Each subcommand is idempotent: a second run changes nothing, so the
// batch integrator runs it again on a merged head (plan §3).
//
//	go run ./scripts/langmigrate tryparens DIR...
//
// tryparens (q105: try may start any operand) removes each (try …) whose
// removal leaves the package's checked meaning the same: the package is
// checked and lowered before and after, and a parenthesis whose removal
// changes the lowered Go (//line positions aside) or the verdict stays.
// It walks each DIR for packages (folders that hold .ergo files) and
// prints one line per rewritten file. Exit 0: done; 2: usage or a folder
// it cannot read.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "tryparens" {
		fmt.Fprintln(stderr, "usage: langmigrate tryparens DIR...")
		return 2
	}
	for _, dir := range args[1:] {
		if err := tryParensTree(dir, stdout); err != nil {
			fmt.Fprintln(stderr, "langmigrate:", err)
			return 2
		}
	}
	return 0
}
