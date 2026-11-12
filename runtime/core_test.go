package runtimefiles

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// corePackages are the runtime packages every lowered program imports.
var corePackages = []string{"runtime/rt", "runtime/rt/rttrace", "runtime/v1"}

// coreRefused are the Go packages, by path prefix, that no core package
// may import: each brings code or an init task of its own (crypto/sha256
// alone was 230 KB of every program, binsize plan R1), so a body that needs
// one is in a leaf of runtime/v1, which only a program that calls it
// imports. fmt and reflect, with runtime/debug that imports fmt, were 70 KB
// of every program (binsize BS2): the core writes a value with rt.Text,
// and the gofmt leaf links fmt only into a program that needs it. net/http
// (the http and rpc leaves) and log/slog (the redacted leaf) import fmt
// and reflect and have init tasks of their own (binsize BS8).
var coreRefused = []string{"crypto/", "encoding/", "hash/", "os/exec", "fmt", "reflect", "runtime/debug", "net", "log"}

// TestCoreHasNoInitRoots: the core packages declare no init function and
// import no package of coreRefused. The Go linker keeps the init task of
// every imported package and every function an init reaches, whether or
// not the program calls into the package, so either would add its code to
// every Ergo program (binsize BS1).
// Mutants: bs1-core-imports-crypto (runtime/v1 imports crypto/sha256),
// bs1-core-init (runtime/v1 declares an init function), and the BS8 leaf
// boundaries bs8-core-imports-net (runtime/v1 imports net/http, as the
// http and rpc bodies did) and bs8-core-imports-log (log/slog, as the
// redacted bodies did).
func TestCoreHasNoInitRoots(t *testing.T) {
	for _, path := range corePackages {
		for _, name := range packages[path].files {
			data, err := sources.ReadFile(strings.TrimPrefix(path, "runtime/") + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			file, err := parser.ParseFile(token.NewFileSet(), name, data, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				imported, _ := strconv.Unquote(spec.Path.Value)
				for _, refused := range coreRefused {
					if strings.HasPrefix(imported, refused) {
						t.Errorf("%s/%s imports %s: a body that needs it belongs in a leaf of runtime/v1", path, name, imported)
					}
				}
			}
			for _, decl := range file.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "init" {
					t.Errorf("%s/%s declares an init function, which every program would run and link", path, name)
				}
			}
		}
	}
}
