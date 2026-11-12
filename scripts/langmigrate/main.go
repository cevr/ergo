// Command langmigrate rewrites Ergo sources for a language change of wave
// 2. Each subcommand is idempotent: a second run changes nothing, so the
// batch integrator runs it again on a merged head (plan §3).
//
//	go run ./scripts/langmigrate tryparens DIR...
//
// tryparens (q105: try may start any operand) removes each (try …) whose
// removal leaves the package's checked meaning the same: the package is
// checked and lowered before and after, and a parenthesis whose removal
// changes the lowered Go or the verdict stays. Positions and spelling are
// not compared: //line comments, columns, the source text an assert
// quotes and the order of two call-site rows on one line. Parentheses
// carry no meaning (q105), so the only pairs that stay are the ones the
// parse needs: round a try whose operand is a binary or catch expression
// inside a tighter operator, as (try a + f()) * 2, where removing them
// regroups the expression; and every pair of a package that does not
// check or lower. It walks each DIR for packages (folders that hold .ergo files) and
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
