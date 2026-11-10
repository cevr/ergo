package check

import (
	"strings"
)

// What ergo inspect reads of a Go package's binding (IG8; docs/tooling.md,
// inspect). The binding is the one source: its header stamps where it came
// from, each declaration carries its Go text in the `// ergo:go` line above
// it, and each declaration it does not import has an `// ergo:refused`
// line with the reason. Nothing here runs a Go tool.

// GoImports are the Go packages that p's files import with import go, each
// once, in the order the files name them.
func GoImports(p *Package) []*Package {
	var out []*Package
	seen := map[*Package]bool{}
	for _, fi := range p.files {
		for _, pn := range fi.imports {
			if pn.Pkg.Host() && !pn.broken && !seen[pn.Pkg] {
				seen[pn.Pkg] = true
				out = append(out, pn.Pkg)
			}
		}
	}
	return out
}

// GoProvenance is where the binding of the Go package p came from: "go1.27
// std (embedded)" for Go's standard library at the compiler's pinned
// toolchain, or the module stamp ("example.com/x v1.2.0", with its replace)
// for a package of a Go module (IG7). It is "" when p is not a Go package.
func GoProvenance(p *Package) string {
	h := hostOf(p)
	if h == nil {
		return ""
	}
	var toolchain string
	for _, line := range strings.SplitN(h.src, "\n", 5) {
		if rest, ok := strings.CutPrefix(line, "// ergo:module "); ok {
			return "module " + rest
		}
		if rest, ok := strings.CutPrefix(line, "// ergo:bind "); ok {
			if f := strings.Fields(rest); len(f) > 1 {
				toolchain = f[1]
			}
		}
	}
	return toolchain + " std (embedded)"
}

// GoRefusals are the declarations the binding of the Go package p does not
// import ("F", or "T.M" for a method), each with its reason.
func GoRefusals(p *Package) map[string]string {
	h := hostOf(p)
	if h == nil {
		return nil
	}
	h.load()
	return h.refused
}

// GoDecl is the Go declaration of a function or method of a Go package as
// Go writes it (its binding's `// ergo:go` line), or "".
func GoDecl(f *Func) string {
	h := hostOf(f.Pkg)
	if h == nil || f.Decl == nil || h.fi == nil {
		return ""
	}
	if h.goLines == nil {
		h.goLines = map[int]string{}
		file := h.fi.syntax
		for _, g := range file.Comments {
			for _, cm := range g.List {
				if strings.HasPrefix(cm.Text, "// ergo:") {
					line, _ := file.Position(cm.Pos())
					h.goLines[line] = cm.Text
				}
			}
		}
	}
	line, _ := h.fi.syntax.Position(f.Decl.Pos())
	for l := line - 1; ; l-- {
		text, ok := h.goLines[l]
		if !ok {
			return ""
		}
		if rest, ok := strings.CutPrefix(text, "// ergo:go "); ok {
			return rest
		}
	}
}

// FuncPosition is the file name and line of f's declaration, or ("", 0)
// when f has no source file.
func FuncPosition(f *Func) (string, int) {
	if f == nil || f.file == nil || f.file.syntax == nil {
		return "", 0
	}
	line, _ := f.file.syntax.Position(f.Pos)
	return f.file.syntax.Name, line
}

// GoForeign reports the signature of a function, method or interface
// operation of a Go package in the foreign tier: its binding charges
// go.Foreign. Every other one is in the reviewed pure tier.
func GoForeign(sig *Signature) bool {
	return sig != nil && goForeignRow(sig.Uses)
}
