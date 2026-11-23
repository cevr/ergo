package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/apigen"
	"ergo.local/prototype/internal/cligen"
	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/llmgen"
	"ergo.local/prototype/internal/mcpgen"
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
)

const genHelp = "usage: ergo gen api [--check] DIR.OPTYPE\n" +
	"       ergo gen rpc [--check] DIR.OPTYPE\n" +
	"       ergo gen cli [--check] DIR.OPTYPE\n" +
	"       ergo gen mcp [--check] DIR.OPTYPE\n" +
	"       ergo gen llm [--check] DIR.OPTYPE\n" +
	"       ergo gen schema [--check] DIR.TYPE\n\n" +
	"ergo gen api writes the hub of the operation type OPTYPE of the package\n" +
	"in DIR (docs/spec/api.md): Ergo source in DIR/<pkg>api/api.ergo, the\n" +
	"operation type as data (Request, Op, Reply, Failure, Outcome), Call, the\n" +
	"one dispatch, Client, a provider over any way to send a Request, and the\n" +
	"laws CallIsTheOperation and ClientIsCall. Each transport below is\n" +
	"written over it, and writes it too: ergo gen rpc, cli, mcp and llm\n" +
	"write the hub beside their own package, and --check checks both.\n\n" +
	"ergo gen rpc writes the Effect RPC server and client: Ergo source over\n" +
	"std rpc in DIR/<pkg>rpc/ (wire.ergo, server.ergo, client.ergo).\n\n" +
	"ergo gen cli writes its command line: Ergo source over std flag in\n" +
	"DIR/<pkg>cli/cli.ergo, a command per operation, Parse, Print, the law\n" +
	"ArgsRoundTrip and Run (docs/spec/api.md, ergo gen cli).\n\n" +
	"ergo gen mcp writes its Model Context Protocol tools: Ergo source over\n" +
	"module mcp in DIR/<pkg>mcp/mcp.ergo, a tool per operation with the JSON\n" +
	"schema of its arguments, Call, Arguments, Answer, Tools and the laws\n" +
	"ArgumentsRoundTrip and ArgumentsFitTheSchema (docs/spec/api.md, ergo gen\n" +
	"mcp).\n\n" +
	"ergo gen llm writes its tools for a language model: Ergo source over\n" +
	"std llm in DIR/<pkg>llm/llm.ergo, a tool spec per operation with the\n" +
	"hub's JSON schema of its arguments, Parse from a model's call to a\n" +
	"Request, Result and Answer from what it ends in to the message that\n" +
	"answers it, and the laws CallRoundTrips and CallsFitTheirSpec\n" +
	"(docs/spec/api.md, ergo gen llm).\n\n" +
	"ergo gen schema writes the JSON schema of a wire type's form: Ergo\n" +
	"source over std jsonschema in DIR/<pkg>schema/<type>.ergo, the\n" +
	"function TYPE() jsonschema.Schema and the law TYPEValuesFitTheSchema\n" +
	"(the codec's JSON of every value fits it; docs/spec/stdlib.md,\n" +
	"jsonschema). It needs no hub.\n\n" +
	"Each generated folder holds ergo.gen, its marker: the generator, the\n" +
	"contract and its digest (or the type it is for), and the digest of the\n" +
	"folder's files; the files carry no header. --check writes nothing and\n" +
	"fails when a file differs from what the contract generates now (a stale\n" +
	"file).\n\n" +
	"Exit codes: 0 written or current; 1 the package does not check, the\n" +
	"contract has no wire or command-line form, or a file is stale; 2\n" +
	"invalid invocation.\n"

// genCommand is `ergo gen api [--check] DIR.OPTYPE` (AF0), and over its hub
// `ergo gen rpc` (R1), `ergo gen cli` (AP1), `ergo gen mcp` (AP3) and `ergo gen
// llm` (wf10 L7); and
// `ergo gen schema [--check] DIR.TYPE` (wf10 JS0), a wire type's schema.
func genCommand(args []string) error {
	if len(args) == 0 || (args[0] != "api" && args[0] != "rpc" && args[0] != "cli" && args[0] != "mcp" && args[0] != "llm" && args[0] != "schema") {
		return usageError{message: "ergo gen writes an operation type's hub or its RPC, CLI, MCP or model-tool glue, or a type's JSON schema: ergo gen api|rpc|cli|mcp|llm [--check] DIR.OPTYPE, ergo gen schema [--check] DIR.TYPE"}
	}
	kind := args[0]
	name := "ergo gen " + kind
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	checkOnly := fs.Bool("check", false, "")
	what := "DIR.OPTYPE"
	if kind == "schema" {
		what = "DIR.TYPE"
	}
	usage := "usage: " + name + " [--check] " + what
	if err := fs.Parse(args[1:]); err != nil {
		return flagError(err, usage)
	}
	if fs.NArg() != 1 {
		return usageError{message: usage}
	}
	arg := fs.Arg(0)
	i := strings.LastIndex(arg, ".")
	if i <= 0 || i == len(arg)-1 {
		if kind == "schema" {
			return usageError{message: name + ": name the type as DIR.TYPE, such as receipt.Receipt"}
		}
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
	out, err := genFiles(kind, root.Package, dir, path, opName)
	if err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
	}
	if *checkOnly {
		if stale := staleFiles(dir, out); len(stale) > 0 {
			return exitStatusError{code: 1, message: fmt.Sprintf("%s: %s is stale; run %s %s", name, strings.Join(stale, ", "), name, arg)}
		}
		return nil
	}
	if err := writeGenFiles(dir, out); err != nil {
		return exitStatusError{code: 1, message: name + ": " + err.Error()}
	}
	return nil
}

// genFiles is what `ergo gen KIND` writes now for the operation type
// opName of the checked package pkg in dir, whose import path is path:
// the hub, a type's schema for schema, and for rpc, cli, mcp and llm the
// kind package written over the hub. A transport writes the hub it names,
// so no order of gen commands can leave it naming a missing or stale one.
func genFiles(kind string, pkg *check.Package, dir, path, opName string) ([]rpcgen.Files, error) {
	if kind == "schema" {
		out, err := genSchemas(pkg, dir, path, opName)
		if err != nil {
			return nil, err
		}
		return []rpcgen.Files{out}, nil
	}
	hub, err := apigen.Generate(pkg, path, opName)
	if err != nil {
		return nil, err
	}
	if kind == "api" {
		return []rpcgen.Files{hub}, nil
	}
	apiPath := path + "/" + hub.Dir
	var out rpcgen.Files
	switch kind {
	case "rpc":
		out, err = rpcgen.Generate(pkg, path, opName, apiPath)
	case "cli":
		out, err = cligen.Generate(pkg, path, opName, apiPath)
	case "llm":
		out, err = llmgen.Generate(pkg, path, opName, apiPath)
	default:
		out, err = mcpgen.Generate(pkg, path, opName, apiPath)
	}
	if err != nil {
		return nil, err
	}
	return []rpcgen.Files{hub, out}, nil
}

// genSchemas owns the schema package as a group: keep its registered
// types and regenerate from the contract, never from on-disk output.
func genSchemas(pkg *check.Package, dir, path, name string) (rpcgen.Files, error) {
	out, err := rpcgen.GenerateSchema(pkg, path, name)
	if err != nil {
		return rpcgen.Files{}, err
	}
	marker, ok, err := genmark.Read(filepath.Join(dir, out.Dir))
	if err != nil {
		return rpcgen.Files{}, err
	}
	if ok && marker.Kind == "schema" {
		for _, subject := range marker.Subjects {
			from, other, _ := genmark.SplitSubject(subject)
			if from != path {
				return rpcgen.Files{}, fmt.Errorf("%s: schema subject %q is not from %s", out.Dir, subject, path)
			}
			if slices.Contains(out.Marker.Subjects, subject) {
				continue
			}
			next, err := rpcgen.GenerateSchema(pkg, path, other)
			if err != nil {
				return rpcgen.Files{}, err
			}
			for file, text := range next.Files {
				if _, exists := out.Files[file]; exists {
					return rpcgen.Files{}, fmt.Errorf("%s: %s and %s generate the same file %s", out.Dir, name, other, file)
				}
				out.Files[file] = text
			}
			out.Marker.Subjects = append(out.Marker.Subjects, subject)
		}
	}
	slices.Sort(out.Marker.Subjects)
	return out, nil
}

func writeGenFiles(dir string, out []rpcgen.Files) error {
	for _, pkg := range out {
		target := filepath.Join(dir, pkg.Dir)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return err
		}
		written := pkg.Written()
		// Publish the marker after the source files.
		for _, file := range append(genNames(pkg.Files), genmark.Name) {
			if err := os.WriteFile(filepath.Join(target, file), []byte(written[file]), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// genNames are the names of a generated folder's files, sorted.
func genNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// staleFiles are the files of out, generated for the package in dir, that
// differ from what dir holds: `ergo gen --check`'s verdict.
func staleFiles(dir string, out []rpcgen.Files) []string {
	var stale []string
	for _, pkg := range out {
		target := filepath.Join(dir, pkg.Dir)
		written := pkg.Written()
		for _, name := range genNames(written) {
			have, err := os.ReadFile(filepath.Join(target, name))
			if err != nil || !bytes.Equal(have, []byte(written[name])) {
				stale = append(stale, filepath.Join(target, name))
			}
		}
	}
	return stale
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
