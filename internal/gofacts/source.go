package gofacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceHash is the first 12 hex digits of the sha256 of target's Go
// declarations (a function of a package of Go's standard library, every
// file of the package, any build constraint), printed by go/printer, in
// the source tree goroot/src. A row whose Source differs is reviewed
// again.
func SourceHash(goroot, target string) (string, error) {
	i := strings.LastIndex(target, ".")
	if i < 0 {
		return "", fmt.Errorf("%s: no package", target)
	}
	pkg, name := target[:i], target[i+1:]
	dir := filepath.Join(goroot, "src", filepath.FromSlash(pkg))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	var texts []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != name {
				continue
			}
			var b bytes.Buffer
			if err := printer.Fprint(&b, fset, fd); err != nil {
				return "", err
			}
			texts = append(texts, b.String())
		}
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("%s: no declaration of %s in %s", target, name, dir)
	}
	sort.Strings(texts)
	sum := sha256.Sum256([]byte(strings.Join(texts, "\n")))
	return hex.EncodeToString(sum[:])[:12], nil
}
