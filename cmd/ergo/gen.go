package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketchfront"
)

const genHelp = "usage: ergo gen rpc [--check] DIR.OPTYPE\n\n" +
	"Writes the Effect RPC server and client of the operation type OPTYPE of\n" +
	"the package in DIR (docs/spec/rpc.md): Ergo source over std rpc in\n" +
	"DIR/<pkg>rpc/ (wire.ergo, server.ergo, client.ergo), each with the\n" +
	"contract's digest. --check writes nothing and fails when a file differs\n" +
	"from what the contract generates now (a stale file).\n\n" +
	"Exit codes: 0 written or current; 1 the package does not check, the\n" +
	"contract has no wire form, or a file is stale; 2 invalid invocation.\n"

// genCommand is `ergo gen rpc [--check] DIR.OPTYPE` (R1).
func genCommand(args []string) error {
	if len(args) == 0 || args[0] != "rpc" {
		return usageError{message: "ergo gen writes RPC glue: ergo gen rpc [--check] DIR.OPTYPE"}
	}
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	checkOnly := fs.Bool("check", false, "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
		return usageError{message: "usage: ergo gen rpc [--check] DIR.OPTYPE"}
	}
	arg := fs.Arg(0)
	i := strings.LastIndex(arg, ".")
	if i <= 0 || i == len(arg)-1 {
		return usageError{message: "ergo gen rpc: name the operation type as DIR.OPTYPE, such as limits.Limiter"}
	}
	dir, opName := arg[:i], arg[i+1:]
	abs, err := filepath.Abs(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo gen rpc: " + err.Error()}
	}
	analysis, err := sketchfront.CheckDir(abs)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo gen rpc: " + err.Error()}
	}
	root := analysis.Root()
	if root.Package == nil {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo gen rpc: %s does not check; run ergo check %s", dir, dir)}
	}
	path, err := importPath(abs)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo gen rpc: " + err.Error()}
	}
	out, err := rpcgen.Generate(root.Package, path, opName)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo gen rpc: " + err.Error()}
	}
	target := filepath.Join(dir, out.Dir)
	names := make([]string, 0, len(out.Files))
	for name := range out.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	if *checkOnly {
		var stale []string
		for _, name := range names {
			have, err := os.ReadFile(filepath.Join(target, name))
			if err != nil || !bytes.Equal(have, []byte(out.Files[name])) {
				stale = append(stale, filepath.Join(target, name))
			}
		}
		if len(stale) > 0 {
			return exitStatusError{code: 1, message: fmt.Sprintf("ergo gen rpc: %s is stale; run ergo gen rpc %s", strings.Join(stale, ", "), arg)}
		}
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return exitStatusError{code: 1, message: "ergo gen rpc: " + err.Error()}
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(target, name), []byte(out.Files[name]), 0o644); err != nil {
			return exitStatusError{code: 1, message: "ergo gen rpc: " + err.Error()}
		}
	}
	return nil
}

// importPath is the import path of the package folder dir: its module's
// path (the ergo.mod of dir or the nearest folder above) and dir's place
// in the module. The checker names the folder it checks main, so the
// generated package's import comes from here.
func importPath(dir string) (string, error) {
	for root := dir; ; {
		text, err := os.ReadFile(filepath.Join(root, "ergo.mod"))
		if err == nil {
			module := ""
			for _, line := range strings.Split(string(text), "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
					module = f[1]
				}
			}
			if module == "" {
				return "", fmt.Errorf("%s has no module line", filepath.Join(root, "ergo.mod"))
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil || rel == "." {
				return "", fmt.Errorf("%s is the module's root; ergo gen rpc reads a package folder inside a module", dir)
			}
			return module + "/" + filepath.ToSlash(rel), nil
		}
		up := filepath.Dir(root)
		if up == root {
			return "", fmt.Errorf("%s is in no module (no ergo.mod above it)", dir)
		}
		root = up
	}
}
