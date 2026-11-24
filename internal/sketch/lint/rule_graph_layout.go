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

// domainImportsImplementation: a domain package imports an
// implementation.
var domainImportsImplementation = &Rule{
	Name:    "domain-imports-implementation",
	Code:    "L157",
	Doc:     "A file of a domain package (one that declares an operation type and has no main), other than a file of tests, imports an implementation (a package that implements another program package's operation type and declares none): a domain package holds contracts, and only a binary's entry chooses implementations.",
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

// rowsOf is the program's rows, built once per run for every graph rule.
func rowsOf(p *Pass) *layoutRows { return p.run.shared.layoutRows(p.Program()) }

func checkFileCycle(p *Pass) {
	lr := rowsOf(p)
	if lr.testCode(p) {
		return
	}
	seen := map[uint32]bool{}
	lr.edgesIn(lr.fileOf(p), graphrows.Uses, func(e graphrows.Edge, off syntax.Pos) {
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
	if lr.testCode(p) {
		return
	}
	lr.edgesIn(lr.fileOf(p), graphrows.Imports, func(e graphrows.Edge, off syntax.Pos) {
		from, to := lr.path(lr.pkg[e.From]), lr.path(e.To)
		la, lb := layerOf(layers, from), layerOf(layers, to)
		if la >= 0 && lb >= 0 && lb < la {
			p.Report(off, off+1, "this import goes up the layering: "+to+" is above "+from)
		}
	})
}

func checkDomainImportsImplementation(p *Pass) {
	// Only a domain package, one that declares a contract, is held to
	// it: a loader, a script host or a server that imports an
	// implementation is wiring, and a choice it makes is a with that
	// with-at-entry reports.
	lr := rowsOf(p)
	if p.Package.Entry() != nil || lr.testCode(p) || !declaresOperation(p.Package) {
		return
	}
	lr.edgesIn(lr.fileOf(p), graphrows.Imports, func(e graphrows.Edge, off syntax.Pos) {
		if to := lr.path(e.To); lr.impls[to] {
			p.Report(off, off+1, "this package declares an operation type and imports "+to+", an implementation of another package's operation type")
		}
	})
}

func checkHiddenDependency(p *Pass) {
	lr := rowsOf(p)
	if lr.testCode(p) {
		return
	}
	lr.edgesIn(lr.fileOf(p), graphrows.Uses, func(e graphrows.Edge, off syntax.Pos) {
		pkg, decl, ok := lr.hiddenReexport(e)
		if !ok {
			return
		}
		fix := reexportFix(p, lr, off, pkg, decl)
		p.ReportFix(off, off+1, "this name reaches "+lr.path(pkg)+" through a re-export, and this package does not import it", fix)
	})
}
