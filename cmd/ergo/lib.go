package main

import (
	"errors"
	"fmt"
	"go/token"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketchfront"
)

const libHelp = "usage: ergo lib FILE|DIR -o DIR\n\n" +
	"Writes the program as a Go library: a Go module in DIR whose module path\n" +
	"and package name are NAME (the file's name without .ergo, or the folder's\n" +
	"name), with each exported function of the root package whose signature Go\n" +
	"can spell as a Go function of the same name taking a context.Context first\n" +
	"(docs/spec/interop.md, Go calls Ergo). A Go program requires NAME and\n" +
	"ergo.local/prototype and replaces them with DIR and DIR/ergo.\n\n" +
	"Exit codes: 0 written; 1 the source does not check or cannot be written;\n" +
	"2 invalid invocation.\n"

// libCommand is `ergo lib FILE|DIR -o DIR` (IG11).
func libCommand(args []string) error {
	var source, out string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-o" && i+1 < len(args):
			out = args[i+1]
			i++
		case len(args[i]) > 0 && args[i][0] == '-':
			return invalidInvocation(fmt.Errorf("unknown option %s", args[i]))
		case source == "":
			source = args[i]
		default:
			return invalidInvocation(errors.New("ergo lib takes one source"))
		}
	}
	if source == "" || out == "" {
		return invalidInvocation(errors.New("usage: ergo lib FILE|DIR -o DIR"))
	}
	l, err := load(source, "go")
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, fileOptions{command: "lib"})
	}
	if !token.IsIdentifier(l.name) {
		return fmt.Errorf("ergo lib: %q is not a Go package name; rename the file or folder", l.name)
	}
	files, err := sketchfront.Library(l.analysis, l.source.Admitted(), l.name, l.name)
	if err != nil {
		return err
	}
	for _, f := range files {
		p := filepath.Join(out, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return err
		}
	}
	fmt.Println(out)
	return nil
}
