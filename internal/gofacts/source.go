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
	// "path.F", or "path.T.M" for a method of T.
	slash := strings.LastIndex(target, "/") + 1
	parts := strings.Split(target[slash:], ".")
	if len(parts) < 2 || len(parts) > 3 {
		return "", fmt.Errorf("%s: not path.F or path.T.M", target)
	}
	pkg, name, recv := target[:slash]+parts[0], parts[len(parts)-1], ""
	if len(parts) == 3 {
		recv = parts[1]
	}
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
			if !ok || fd.Name.Name != name || recvName(fd) != recv {
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

// recvName is the name of fd's receiver type (T for T and *T), or "" for a
// function.
func recvName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if ix, ok := t.(*ast.IndexExpr); ok {
		t = ix.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}
