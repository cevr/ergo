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

	"ergo.local/prototype/internal/cligen"
	"ergo.local/prototype/internal/mcpgen"
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
)

const genHelp = "usage: ergo gen rpc [--check] DIR.OPTYPE\n" +
	"       ergo gen cli [--check] DIR.OPTYPE\n" +
	"       ergo gen mcp [--check] DIR.OPTYPE\n\n" +
	"ergo gen rpc writes the Effect RPC server and client of the operation\n" +
	"type OPTYPE of the package in DIR (docs/spec/rpc.md): Ergo source over\n" +
	"std rpc in DIR/<pkg>rpc/ (wire.ergo, server.ergo, client.ergo).\n\n" +
	"ergo gen cli writes its command line: Ergo source over std flag in\n" +
	"DIR/<pkg>cli/cli.ergo, a command per operation, Parse, Print, the law\n" +
	"ArgsRoundTrip and Run (docs/spec/rpc.md, ergo gen cli). When DIR/<pkg>rpc\n" +
	"exists, the cli package names its Request; it must be current.\n\n" +
	"ergo gen mcp writes its Model Context Protocol tools: Ergo source over\n" +
	"module mcp in DIR/<pkg>mcp/mcp.ergo, a tool per operation with the JSON\n" +
	"schema of its arguments, Call, Arguments, Answer, Tools and the laws\n" +
	"ArgumentsRoundTrip and ArgumentsFitTheSchema (docs/spec/rpc.md, ergo gen\n" +
	"mcp). It names DIR/<pkg>rpc's Request: run ergo gen rpc first.\n\n" +
	"Each file carries the contract's digest. --check writes nothing and\n" +
	"fails when a file differs from what the contract generates now (a stale\n" +
	"file).\n\n" +
	"Exit codes: 0 written or current; 1 the package does not check, the\n" +
	"contract has no wire or command-line form, or a file is stale; 2\n" +
	"invalid invocation.\n"

// genCommand is `ergo gen rpc [--check] DIR.OPTYPE` (R1) and `ergo gen cli
// [--check] DIR.OPTYPE` (AP1) and `ergo gen mcp [--check] DIR.OPTYPE` (AP3).
func genCommand(args []string) error {
	if len(args) == 0 || (args[0] != "rpc" && args[0] != "cli" && args[0] != "mcp") {
		return usageError{message: "ergo gen writes RPC, CLI or MCP glue: ergo gen rpc|cli|mcp [--check] DIR.OPTYPE"}
	}
	kind := args[0]
	name := "ergo gen " + kind
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	checkOnly := fs.Bool("check", false, "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
		return usageError{message: "usage: " + name + " [--check] DIR.OPTYPE"}
	}
	arg := fs.Arg(0)
	i := strings.LastIndex(arg, ".")
	if i <= 0 || i == len(arg)-1 {
		return usageError{message: name + ": name the operation type as DIR.OPTYPE, such as limits.Limiter"}
	}
	dir, opName := arg[:i], arg[i+1:]
	abs, err := filepath.Abs(dir)
	if err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
	}
	analysis, err := sketchfront.CheckDir(abs)
	if err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
	}
	root := analysis.Root()
	if root.Package == nil {
		return exitStatusError{code: 1, message: fmt.Sprintf("%s: %s does not check; run ergo check %s", name, dir, dir)}
	}
	path, err := importPath(abs)
	if err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
	}
	var out rpcgen.Files
	switch kind {
	case "rpc":
		out, err = rpcgen.Generate(root.Package, path, opName)
	case "cli":
		out, err = genCLI(root.Package, dir, path, opName)
	default:
		out, err = genMCP(root.Package, dir, path, opName)
	}
	if err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
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
			return exitStatusError{code: 1, message: fmt.Sprintf("%s: %s is stale; run %s %s", name, strings.Join(stale, ", "), name, arg)}
		}
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
	}
	for _, file := range names {
		if err := os.WriteFile(filepath.Join(target, file), []byte(out.Files[file]), 0o644); err != nil {
			return exitStatusError{code: 1, message: name + ": " + err.Error()}
		}
	}
	return nil
}

// genCLI is the cli package of the contract: over the rpc package's
// Request when DIR/<pkg>rpc exists, which must then be what ergo gen rpc
// writes now, so the two packages name one Request.
func genCLI(pkg *check.Package, dir, path, opName string) (rpcgen.Files, error) {
	rpcPath, err := currentRPC(pkg, dir, path, opName, "cli")
	if err != nil {
		return rpcgen.Files{}, err
	}
	return cligen.Generate(pkg, path, opName, rpcPath)
}

// genMCP is the mcp package of the contract, over the rpc package's
// Request, which must exist and be current.
func genMCP(pkg *check.Package, dir, path, opName string) (rpcgen.Files, error) {
	rpcPath, err := currentRPC(pkg, dir, path, opName, "mcp")
	if err != nil {
		return rpcgen.Files{}, err
	}
	return mcpgen.Generate(pkg, path, opName, rpcPath)
}

// currentRPC is the import path of DIR/<pkg>rpc when it exists, "" when it
// does not; a file of it that differs from what ergo gen rpc writes now
// fails, since the kind package names its Request.
func currentRPC(pkg *check.Package, dir, path, opName, kind string) (string, error) {
	glue, err := rpcgen.Generate(pkg, path, opName)
	if err != nil {
		return "", err
	}
	rpcDir := filepath.Join(dir, glue.Dir)
	info, err := os.Stat(rpcDir)
	if err != nil || !info.IsDir() {
		return "", nil
	}
	for file, text := range glue.Files {
		have, err := os.ReadFile(filepath.Join(rpcDir, file))
		if err != nil || !bytes.Equal(have, []byte(text)) {
			return "", fmt.Errorf("%s is stale, and the %s package names its Request; run ergo gen rpc first", filepath.Join(rpcDir, file), kind)
		}
	}
	return path + "/" + glue.Dir, nil
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
				return "", fmt.Errorf("%s is the module's root; ergo gen reads a package folder inside a module", dir)
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
