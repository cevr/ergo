// Command migrate196 translates old-syntax Ergo sources into the decided
// syntax of the v1 language (internal/migrate196). One-off; deleted after v1.
//
//	migrate196 FILE                  write the translation to stdout
//	migrate196 -w FILE...            rewrite each file in place
//	migrate196 -gostrings FILE...    translate the sources embedded in Go files
//	migrate196 -md FILE...           translate the go fences of Markdown files
//
// Notes that need a hand edit go to stderr as path:line: note. Exit 0: the
// translation has no notes. Exit 1: notes. Exit 2: usage error, or an input
// that does not parse as old syntax.
package main

import (
	"fmt"
	"os"

	"ergo.local/prototype/internal/migrate196"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		return usage()
	}
	mode := ""
	switch args[0] {
	case "-w", "-gostrings", "-md":
		mode, args = args[0], args[1:]
		if len(args) == 0 {
			return usage()
		}
	default:
		if len(args) != 1 || len(args[0]) > 0 && args[0][0] == '-' {
			return usage()
		}
	}
	code := 0
	for _, path := range args {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		var out string
		var notes []migrate196.Note
		switch mode {
		case "-gostrings":
			out, notes, err = migrate196.GoStrings(string(data))
		case "-md":
			out, notes = migrate196.Markdown(string(data))
		default:
			out, notes, err = migrate196.Translate(string(data))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			return 2
		}
		for _, n := range notes {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", path, n.Line, n.Text)
		}
		if len(notes) > 0 {
			code = 1
		}
		if mode == "" {
			fmt.Print(out)
			continue
		}
		if len(notes) > 0 && mode == "-w" {
			continue
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return code
}

func usage() int {
	fmt.Fprintln(os.Stderr, "usage: migrate196 FILE | -w FILE... | -gostrings FILE... | -md FILE...")
	return 2
}
