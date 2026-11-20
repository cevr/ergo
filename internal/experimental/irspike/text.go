package irspike

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sketch/lower"
	v1 "ergo.local/prototype/runtime/v1"
)

// GoText builds files (a LowerIR or LowerIRTests module) to out with the
// derivative toolchain tc (`ergo build -go-text` passes the patched
// release of ergo's cache, internal/toolchain Patch), and writes into dir,
// which must not exist, the module with each IR body printed as Go text
// (lower.IRGoText): go vet, gopls and a reader see the IR bodies as Go,
// at their .ergo positions. The text is a view, never an input: the build
// compiles the IR.
func GoText(files []lower.File, tc Toolchain, out, dir string, stderr io.Writer) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s exists: the Go text goes into a new directory", dir)
	}
	work, err := os.MkdirTemp("", "ergo-go-text-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	mod := filepath.Join(work, "ir")
	if err := writeModule(mod, files); err != nil {
		return err
	}
	if _, err := Build(tc, IR, mod, out, BuildOptions{Stderr: stderr}); err != nil {
		return err
	}
	text, err := printText(tc, mod, files)
	if err != nil {
		return err
	}
	return writeModule(dir, text)
}

// printText is files with their IR bodies printed. The packages the IR
// packages import are read from their export data, which the build of the
// module files wrote to mod left in tc's cache.
func printText(tc Toolchain, mod string, files []lower.File) ([]lower.File, error) {
	imp, err := exportImporter(tc, mod, files)
	if err != nil {
		return nil, err
	}
	return lower.IRGoText(files, imp)
}

// exportImporter imports, from gc export data, the packages that the
// module's IR packages import and that hold no IR: go list -export of
// them in mod, with the build's toolchain, cache and flags.
func exportImporter(tc Toolchain, mod string, files []lower.File) (types.Importer, error) {
	ir := map[string]bool{} // the IR packages' directories
	for _, f := range files {
		if path.Base(f.Path) == lower.IRFileName {
			ir[path.Dir(f.Path)] = true
		}
	}
	irPath := func(dir string) string {
		if dir == "." {
			return v1.ProgramModule
		}
		return v1.ProgramModule + "/" + dir
	}
	isIR := map[string]bool{}
	for d := range ir {
		isIR[irPath(d)] = true
	}
	var imports []string
	fset := token.NewFileSet()
	for _, f := range files {
		if !ir[path.Dir(f.Path)] || !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		af, err := parser.ParseFile(fset, f.Path, f.Data, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, spec := range af.Imports {
			if p := strings.Trim(spec.Path.Value, `"`); !isIR[p] && p != "unsafe" && !slices.Contains(imports, p) {
				imports = append(imports, p)
			}
		}
	}
	exports := map[string]string{}
	if len(imports) > 0 {
		cmd, err := Command(tc, Text, mod, "", BuildOptions{})
		if err != nil {
			return nil, err
		}
		args := append([]string{"list"}, receipt.GoBuildFlags...)
		args = append(args, "-deps", "-export", "-json=ImportPath,Export")
		cmd.Args = append(append(cmd.Args[:1], args...), imports...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("go list -export: %v: %s", err, strings.TrimSpace(stderr.String()))
		}
		dec := json.NewDecoder(&stdout)
		for dec.More() {
			var p struct{ ImportPath, Export string }
			if err := dec.Decode(&p); err != nil {
				return nil, err
			}
			exports[p.ImportPath] = p.Export
		}
	}
	return importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(file)
	}), nil
}
