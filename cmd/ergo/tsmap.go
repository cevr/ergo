package main

import (
	"fmt"
	"os"

	sourcefile "ergo.local/prototype/internal/source"
	"ergo.local/prototype/internal/tsproj"
)

const tsmapHelp = "usage: ergo tsmap [--emit-js FILE.ergo]\n\n" +
	"Without arguments, serve the TypeScript 7.1 beta content mapper on stdio.\n" +
	"--emit-js writes the same checked wire schemas as JavaScript for a loader.\n" +
	"It does not compile Ergo functions or start TypeScript. See tools/ts/README.md.\n\n" +
	"Exit codes: 0 success; 1 source, projection or protocol failure; 2 invalid invocation.\n"

func tsmapCommand(args []string) error {
	if len(args) == 0 {
		return tsproj.Serve(os.Stdin, os.Stdout)
	}
	if len(args) != 2 || args[0] != "--emit-js" {
		return usageError{message: "usage: ergo tsmap [--emit-js FILE.ergo]"}
	}
	b, err := sourcefile.ReadRegularFile(args[1], 0)
	if err != nil {
		return err
	}
	o, err := tsproj.Transform(args[1], string(b), tsproj.Options{JavaScript: true})
	if err != nil {
		return err
	}
	if len(o.Diagnostics) > 0 {
		return fmt.Errorf("%s: %s", args[1], o.Diagnostics[0].Message)
	}
	_, err = fmt.Fprint(os.Stdout, o.Text)
	return err
}
