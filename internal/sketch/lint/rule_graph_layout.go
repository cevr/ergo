package lint

import (
	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The graph rules of the layout preset (CI-SELECT §12.6 layer 1,
// GRAPH-LINT), each over the program's graph rows (graphlayout.go). A
// package cycle through test imports is no rule: tests live in their
// package's files, and the loader refuses every import cycle (S108), so
// a checked program has none.

// fileCycle (opt-in, in no preset): files of one package that use each
// other in a cycle.
var fileCycle = &Rule{
	Name:    "file-cycle",
	Code:    "L155",
	Doc:     "A file uses a declaration of another file of its package that, through its own uses, comes back to this file: the files are one unit split in two.",
	Instead: "move the declarations the two files share into one of them, or into a file both use",
	Default: Hint,
	Check:   checkFileCycle,
}

// layer: an import that goes up the configured layering.
var layer = &Rule{
	Name:    "layer",
	Code:    "L156",
	Doc:     "An import goes up the layering the configuration declares (option layers: package patterns from the top down, P/... for every package under P).",
	Instead: "move what the lower package needs from the upper one down, or pass it in",
	Default: Warning,
	Check:   checkLayer,
	Options: options(func() layerOptions { return layerOptions{} }, nil),
}

// domainImportsImplementation: a package other than an entry imports an
// implementation.
var domainImportsImplementation = &Rule{
	Name:    "domain-imports-implementation",
	Code:    "L157",
	Doc:     "A file of a package with no main that is no implementation itself, other than a file of tests, imports a package that implements another program package's operation type and declares none: only a binary's entry chooses implementations.",
	Instead: "need the operation type, and bind the implementation in the binary's main",
	Default: Warning,
	Check:   checkDomainImportsImplementation,
}

// hiddenDependency: a use of a declaration through a let of another
// package that re-exports it, without an import of its own package.
var hiddenDependency = &Rule{
	Name:    "hidden-dependency",
	Code:    "L158",
	Doc:     "A name is used through a let of another package that re-exports it, and the file's package does not import the package that declares it.",
	Instead: "name the declaration itself and import its package",
	Default: Warning,
	Check:   checkHiddenDependency,
	SafeFix: true,
}

// rowsOf is the program's rows, built once per rule and run.
func rowsOf(p *Pass) *layoutRows {
	return p.Memo(func() any { return layoutRowsOf(p.Program()) }).(*layoutRows)
}

func checkFileCycle(p *Pass) {
	lr := rowsOf(p)
	seen := map[uint32]bool{}
	lr.edgesIn(p.Rel(), graphrows.Uses, func(e graphrows.Edge, off syntax.Pos) {
		a, b := lr.file[e.From], lr.file[e.To]
		if b == graphrows.NoNode || a == b || seen[b] || lr.pkg[a] != lr.pkg[b] || !lr.fileReaches(b, a) {
			return
		}
		seen[b] = true
		p.Report(off, off+1, "this use reaches "+lr.t.Str(lr.t.Nodes[b].Name)+", whose uses come back to this file")
	})
}

func checkLayer(p *Pass) {
	layers := p.Options.(layerOptions).Layers
	lr := rowsOf(p)
	lr.edgesIn(p.Rel(), graphrows.Imports, func(e graphrows.Edge, off syntax.Pos) {
		from, to := lr.path(lr.pkg[e.From]), lr.path(e.To)
		la, lb := layerOf(layers, from), layerOf(layers, to)
		if la >= 0 && lb >= 0 && lb < la {
			p.Report(off, off+1, "this import goes up the layering: "+to+" is above "+from)
		}
	})
}

func checkDomainImportsImplementation(p *Pass) {
	lr := rowsOf(p)
	impls := lr.impls
	// An implementation may build on another (an RPC loopback on the
	// generated client): an entry chooses it as a whole.
	if p.Package.Entry() != nil || testFile(p.File) || impls[lr.paths[p.Package]] {
		return
	}
	lr.edgesIn(p.Rel(), graphrows.Imports, func(e graphrows.Edge, off syntax.Pos) {
		if to := lr.path(e.To); impls[to] {
			p.Report(off, off+1, "this package has no main and imports "+to+", which implements another package's operation type")
		}
	})
}

func checkHiddenDependency(p *Pass) {
	lr := rowsOf(p)
	lr.edgesIn(p.Rel(), graphrows.Uses, func(e graphrows.Edge, off syntax.Pos) {
		pkg, decl, ok := lr.hiddenReexport(e)
		if !ok {
			return
		}
		fix := reexportFix(p, lr, off, pkg, decl)
		p.ReportFix(off, off+1, "this name reaches "+lr.path(pkg)+" through a re-export, and this package does not import it", fix)
	})
}
