package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"ergo.local/prototype/internal/sketch/fix"
)

const fixHelp = "usage: ergo fix [--dry-run] [--std DIR] [--go] PATH...\n\n" +
	"Rewrites source that an older rule accepted into the spelling the language\n" +
	"now requires (internal/sketch/fix). It writes the declared implementations:\n" +
	"`implements I` on each type that the old method-set rule used as the\n" +
	"operation type I, and `..I` in each operation type used as I. A PATH is a\n" +
	"file or a folder; a folder is walked, each module package by package and\n" +
	"each other .ergo file as a program of its own. --std DIR also checks every\n" +
	"package of the embedded standard library and edits its source in DIR when\n" +
	"DIR is under a PATH. It prints what it wrote and each pair it could not\n" +
	"write, with the repair. --go also reads the Ergo programs that .go files\n" +
	"hold in string literals, each as a program of its own, and edits the\n" +
	"literals. --dry-run writes nothing.\n\n" +
	"Exit codes: 0 done; 1 a file could not be read or written; 2 invalid\n" +
	"invocation.\n"

// fixCommand is `ergo fix [--dry-run] [--std DIR] PATH...`.
func fixCommand(args []string) error {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("dry-run", false, "")
	stdRoot := fs.String("std", "", "")
	goLits := fs.Bool("go", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return usageError{message: "usage: ergo fix [--dry-run] [--std DIR] [--go] PATH..."}
	}
	rep, err := fix.Run(fix.Options{Paths: fs.Args(), StdRoot: *stdRoot, Write: !*dry, Go: *goLits})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	fmt.Fprint(os.Stdout, rep.Text())
	return nil
}
