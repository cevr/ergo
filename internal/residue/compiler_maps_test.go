package residue_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestCompilerHasNoPointerKeyedMaps holds the self-hosted compiler's data
// rule (compile-speed plan §5.2, capstone §3a amendment 3): the Ergo module
// `compiler/` keys no Map by a node, a type or a symbol. Per-node, per-type
// and per-symbol results are dense side tables indexed by dense ids, as
// Zig's InternPool and cranelift-entity's SecondaryMap do; the Go checker's
// 397 pointer-keyed maps are the cost this rule keeps out. A Map key in
// compiler/ is a builtin or std type, or a type compiler/ defines over
// string (a path, a name); never a borrow, never a record, union, operation
// type or machine compiler/ declares (a node, a type or a symbol as a
// value), and never a dense id: a defined integer compiler/ declares, or
// std dense's ID[K] or Opt[K] (the store's ids, capstone S3a). The
// module does not exist before S1 of the capstone; the rule holds from its
// first file.
func TestCompilerHasNoPointerKeyedMaps(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(strings.TrimSpace(string(root)), "compiler")
	if _, err := os.Stat(dir); err != nil {
		t.Logf("no compiler/ module yet: %v", err)
		return
	}
	findings, err := compilerMapKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Error(f)
	}
}

// compilerMapKeys returns one line per Map in the Ergo module at dir whose
// key the rule refuses: `file:line:col: Map keyed by K (what K is)`.
func compilerMapKeys(dir string) ([]string, error) {
	module := ""
	if data, err := os.ReadFile(filepath.Join(dir, "ergo.mod")); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				module = strings.TrimSpace(name)
			}
		}
	}
	type file struct {
		path, folder string
		ast          *syntax.File
	}
	var files []file
	// kinds names each declared type by its folder and name: what it is, or
	// "" for a type defined over string, which may key a map.
	kinds := map[[2]string]string{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".ergo") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := syntax.Parse(string(src))
		if err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		folder := filepath.Dir(path)
		files = append(files, file{path, folder, f})
		for _, d := range f.Decls {
			if td, ok := d.(*syntax.TypeDecl); ok && td.Name != nil {
				kinds[[2]string{folder, td.Name.Name}] = declaredKind(td.Spec)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var findings []string
	for _, f := range files {
		// Each import of the module names the folder its qualifier reads;
		// stdDense is the qualifier of std dense in this file, if imported.
		folders := map[string]string{}
		stdDense := ""
		for _, d := range f.ast.Decls {
			id, ok := d.(*syntax.ImportDecl)
			if !ok {
				continue
			}
			for _, s := range id.Specs {
				// A decided import path is unquoted; an older one is a string.
				path := s.Path.Value
				if p, err := strconv.Unquote(path); err == nil {
					path = p
				}
				if s.Kind == syntax.ImportGo {
					continue
				}
				if path == "dense" {
					stdDense = "dense"
					if s.Kind == syntax.ImportNamed && s.Name != nil {
						stdDense = s.Name.Name
					}
					continue
				}
				if module == "" {
					continue
				}
				rest, ok := strings.CutPrefix(path, module+"/")
				if !ok {
					continue
				}
				name := rest[strings.LastIndex(rest, "/")+1:]
				if s.Kind == syntax.ImportNamed && s.Name != nil {
					name = s.Name.Name
				}
				folders[name] = filepath.Join(dir, filepath.FromSlash(rest))
			}
		}
		syntax.Inspect(f.ast, func(n syntax.Node) bool {
			m, ok := n.(*syntax.MapType)
			if !ok {
				return true
			}
			what := ""
			switch k := m.Key.(type) {
			case *syntax.StarExpr:
				what = "a borrow"
			case *syntax.Ident:
				what = kinds[[2]string{f.folder, k.Name}]
			case *syntax.SelectorExpr:
				if q, ok := k.X.(*syntax.Ident); ok && folders[q.Name] != "" {
					what = kinds[[2]string{folders[q.Name], k.Sel.Name}]
				}
			case *syntax.IndexExpr:
				// std dense's ID[K] and Opt[K] are dense ids of any kind.
				if s, ok := k.X.(*syntax.SelectorExpr); ok && (s.Sel.Name == "ID" || s.Sel.Name == "Opt") {
					if q, ok := s.X.(*syntax.Ident); ok && stdDense != "" && q.Name == stdDense {
						what = "a dense id"
					}
				}
			}
			if what != "" {
				line, col := f.ast.Position(m.Key.Pos())
				findings = append(findings, fmt.Sprintf("%s:%d:%d: Map keyed by %s (%s): use a dense side table indexed by the id", f.path, line, col, f.ast.Src[int(m.Key.Pos()):int(m.Key.End())], what))
			}
			return true
		})
	}
	slices.Sort(findings)
	return findings, nil
}

// declaredKind is what a declared type is, as a map key: "" for a type
// defined over string, else the reason a map may not be keyed by it.
func declaredKind(spec syntax.Expr) string {
	switch s := spec.(type) {
	case *syntax.Ident:
		switch s.Name {
		case "string":
			return ""
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
			return "a dense id"
		}
		return "a defined type"
	case *syntax.StructType:
		return "a record"
	case *syntax.InterfaceType:
		if len(s.Union) > 0 {
			return "a union"
		}
		return "an operation type"
	}
	return "a defined type"
}
