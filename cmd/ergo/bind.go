package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketchfront"
)

const bindHelp = "usage: ergo bind go [-o DIR] PACKAGE...\n\n" +
	"Writes the Ergo binding of each Go package: its exported declarations with\n" +
	"their Ergo signatures and rows, and the reason for each one that does not\n" +
	"map (docs/spec/interop.md).\n\n" +
	"Inside an Ergo module (an ergo.mod in the current folder or above), it runs\n" +
	"go list in the module root, against the go.mod and go.sum beside ergo.mod,\n" +
	"and writes each binding to <module root>/ergo.bind/<import path>/<last\n" +
	"element>.ergo, stamped with the Go module's version, with the binding of\n" +
	"every package outside Go's standard library that one names. A package of\n" +
	"Go's standard library needs no binding there: its binding is embedded.\n" +
	"Outside a module, it runs go list in the current folder and prints the\n" +
	"bindings. With -o, each binding is written to DIR/<import path>/<last\n" +
	"element>.ergo instead.\n\n" +
	"Exit codes: 0 written; 1 go list or a binding failed; 2 invalid invocation.\n"

// bindCommand is `ergo bind go [-o DIR] PACKAGE...` (#202; IG7).
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
	dir, module, err := bindRoot()
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
	}
	env := os.Environ()
	v, err := gobind.GoVersion(env)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
	}
	files, err := gobind.Bind(dir, env, v, fs.Args()...)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
	}
	switch {
	case *out != "":
		if err := gobind.WriteAll(*out, files); err != nil {
			return exitStatusError{code: 1, message: "ergo bind: " + err.Error()}
		}
		return nil
	case module:
		for path, text := range files {
			if !strings.Contains(text, "\n"+gobind.ModuleLine) {
				return exitStatusError{code: 1, message: "ergo bind: " + path + " is in Go's standard library, whose binding is embedded; a module binds packages of its go.mod only"}
			}
		}
		if err := gobind.WriteAll(filepath.Join(dir, "ergo.bind"), files); err != nil {
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

// bindRoot is where go list runs: the root of the Ergo module above the
// current folder, which must hold go.mod beside ergo.mod (module true), or
// else the current folder.
func bindRoot() (string, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, err
	}
	for d := cwd; ; {
		if info, err := os.Stat(filepath.Join(d, sketchfront.ModFile)); err == nil && info.Mode().IsRegular() {
			if _, err := os.Stat(filepath.Join(d, sketchfront.GoModFile)); errors.Is(err, fs.ErrNotExist) {
				return "", false, fmt.Errorf("the module at %s has no go.mod beside ergo.mod; run go mod init there, then go get each Go module it binds (docs/spec/interop.md, Go modules)", d)
			}
			return d, true, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ".", false, nil
		}
		d = parent
	}
}
