package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"ergo.local/prototype/internal/sketch/lawfix"
)

const fixHelp = "usage: ergo fix [--dry-run] [--std DIR] PATH...\n\n" +
	"Rewrites source that an older rule accepted into the form the language\n" +
	"now requires. It moves each top-level law that does not sit in a file of\n" +
	"its subjects (S115; types.md, Laws 15) beside its main subject, with\n" +
	"each law helper beside the first law or test that uses it and each\n" +
	"provider the laws run against beside their operation type; a file of\n" +
	"laws left holding other declarations is named by its concept\n" +
	"(internal/sketch/lawfix). A PATH is a package\n" +
	"folder, or DIR/... for every package of its module below DIR. --std DIR\n" +
	"also checks each package of the standard library from its source in\n" +
	"DIR and edits it there. It prints each move, each file it deleted (no\n" +
	"declaration left) and each law that names nothing in its package.\n" +
	"--dry-run writes nothing.\n\n" +
	"Exit codes: 0 done; 1 a file could not be read or written; 2 invalid\n" +
	"invocation.\n"

// fixCommand is `ergo fix [--dry-run] [--std DIR] PATH...`.
func fixCommand(args []string) error {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("dry-run", false, "")
	stdRoot := fs.String("std", "", "")
	if err := fs.Parse(args); err != nil || (fs.NArg() == 0 && *stdRoot == "") {
		return usageError{message: "usage: ergo fix [--dry-run] [--std DIR] PATH..."}
	}
	rep, err := lawfix.Run(lawfix.Options{Paths: fs.Args(), StdRoot: *stdRoot, Write: !*dry})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	fmt.Fprint(os.Stdout, rep.Text())
	return nil
}
