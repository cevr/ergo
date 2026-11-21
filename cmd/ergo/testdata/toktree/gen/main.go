// Command gen writes the input of examples/toktree's workload: exactly N
// bytes of the repository's own Ergo source, the .ergo files under
// internal/sketch/std and examples in lexical path order, repeated as
// often as N needs and cut at N bytes. Run it from the repository root:
//
//	gen N
//
// The text is the self-layout plan's corpus kind (Ergo as the compiler
// reads it), so the receipt's token and row mix is real; it follows the
// tree, so a receipt names its commit.
package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	var files []string
	for _, root := range []string{"internal/sketch/std", "examples"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(path, ".ergo") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "gen: no .ergo files; run from the repository root")
		os.Exit(1)
	}
	w := bufio.NewWriterSize(os.Stdout, 1<<16)
	left := n
	for left > 0 {
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				fmt.Fprintln(os.Stderr, "gen:", err)
				os.Exit(1)
			}
			if len(data) > left {
				data = data[:left]
			}
			w.Write(data)
			left -= len(data)
			if left == 0 {
				break
			}
		}
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
