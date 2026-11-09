package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"ergo.local/prototype/internal/gobind"
)

const bindHelp = "usage: ergo bind go [-o DIR] PACKAGE...\n\n" +
	"Writes the Ergo binding of each Go package: its exported declarations with\n" +
	"their Ergo signatures and rows, and the reason for each one that does not\n" +
	"map (docs/spec/interop.md). It runs go list in the current directory.\n" +
	"With -o, each binding is written to DIR/<import path>/<last element>.ergo;\n" +
	"without it, the bindings are printed.\n\n" +
	"Exit codes: 0 written; 1 go list or a binding failed; 2 invalid invocation.\n"

// bindCommand is `ergo bind go [-o DIR] PACKAGE...` (#202).
func bindCommand(args []string) error {
	if len(args) == 0 || args[0] != "go" {
		return usageError{message: "ergo bind binds Go packages: ergo bind go [-o DIR] PACKAGE..."}
	}
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("o", "", "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() == 0 {
		return usageError{message: "usage: ergo bind go [-o DIR] PACKAGE..."}
	}
	env := os.Environ()
	v, err := gobind.GoVersion(env)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
	}
	files, err := gobind.Bind(".", env, v, fs.Args()...)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
	}
	if *out != "" {
		if err := gobind.WriteAll(*out, files); err != nil {
			return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
		}
		return nil
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Print(files[p])
	}
	return nil
}
