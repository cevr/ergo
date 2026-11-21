// Command gen writes the input of examples/tn1-lint's workload: exactly N
// bytes of TypeScript, the conformance vectors' TypeScript sources
// (modules/tn1/testdata/vectors/src/*.ts.txt) in lexical order, repeated
// as often as N needs and cut at N bytes. Run it from the repository root:
//
//	gen N
//
// The text holds every construct the record reads (templates, regular
// expressions, imports, handlers, doc and directive comments), so the
// receipt's mix of tokens and lines is the record's own.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gen N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[1])
	if err != nil || n <= 0 {
		fmt.Fprintln(os.Stderr, "gen: N must be a positive count of bytes")
		os.Exit(2)
	}
	files, err := filepath.Glob("modules/tn1/testdata/vectors/src/*.ts.txt")
	if err != nil || len(files) == 0 {
		fmt.Fprintln(os.Stderr, "gen: no TypeScript vectors under modules/tn1/testdata/vectors/src (run from the repository root)")
		os.Exit(1)
	}
	var text []byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		text = append(text, b...)
	}
	w := bufio.NewWriter(os.Stdout)
	for left := n; left > 0; {
		k := min(left, len(text))
		w.Write(text[:k])
		left -= k
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
