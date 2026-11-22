package lint

import (
	"cmp"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The change rules (anti-slop AS6) read the run's ChangeSet, so they
// report only under --diff: without a change there is nothing added.

// addedClone is a function the change adds whose shape (shapeOf: what it
// does, with its locals' names left out) is that of another function of
// the program: the change wrote again what the program has. A function
// moved from one file to another is no clone: the program holds one copy.
var addedClone = &Rule{
	Name:    "added-clone",
	Code:    "L130",
	Doc:     "A function the change adds does what another function of the program does; call that one.",
	Instead: "call the existing function",
	Default: Warning,
	Check:   checkAddedClone,
}

// scatteredEdit is one edit inside bodies, by shape (shapeOfEdit), that
// the change adds in three or more packages: a concept the program spells in each package instead of
// once (shotgun surgery, the exact form).
var scatteredEdit = &Rule{
	Name:    "scattered-edit",
	Code:    "L131",
	Doc:     "The change adds the same code in three or more packages; write it once where they all reach it.",
	Instead: "write it once in a package they all import",
	Default: Warning,
	Check:   checkScatteredEdit,
}

// scatteredMin is how many packages make an edit scattered, and
// scatteredLines the least lines a hunk's code spans to count, so a
// one-line edit (an import, a field) is not a concept.
const (
	scatteredMin   = 3
	scatteredLines = 3
)

// funcShape is one function of the program with a body: where it is, its
// name and its shape.
type funcShape struct {
	rel   string
	name  *syntax.Ident
	shape string
	added bool
}

func checkAddedClone(p *Pass) {
	if p.Change() == nil {
		return
	}
	twins := p.Memo(func() any { return addedClones(p) }).(map[*syntax.Ident]funcShape)
	for _, d := range p.File.Decls {
		if f, ok := d.(*syntax.FuncDecl); ok {
			if twin, ok := twins[f.Name]; ok {
				where := ""
				if twin.rel != p.Rel() {
					where = " in " + twin.rel
				}
				p.Report(f.Name.Pos(), f.Name.End(), fmt.Sprintf("%s, which this change adds, does what %s%s does", f.Name.Name, twin.name.Name, where))
			}
		}
	}
}

// addedClones maps each added function with a twin to its first twin
// other than itself, by file and name.
func addedClones(p *Pass) map[*syntax.Ident]funcShape {
	added := p.Change().Added
	var all []funcShape
	for _, u := range p.Program() {
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, d := range f.Syntax.Decls {
				fd, ok := d.(*syntax.FuncDecl)
				if !ok || fd.Body == nil || fd.Extern != nil || fd.Derived != nil {
					continue
				}
				from, to := f.Syntax.Line(fd.Pos()), f.Syntax.Line(fd.End())
				all = append(all, funcShape{
					rel:   f.Rel,
					name:  fd.Name,
					shape: shapeOf(u.Package.Info, syntax.Node(fd.TypeParams), syntax.Node(fd.Type), syntax.Node(fd.Body)),
					added: within(added[f.Rel], from, to),
				})
			}
		}
	}
	slices.SortStableFunc(all, func(a, b funcShape) int {
		return cmp.Or(cmp.Compare(a.rel, b.rel), cmp.Compare(a.name.Name, b.name.Name))
	})
	out := map[*syntax.Ident]funcShape{}
	for _, s := range all {
		if !s.added {
			continue
		}
		for _, t := range all {
			if t.name != s.name && t.shape == s.shape {
				out[s.name] = t
				break
			}
		}
	}
	return out
}

// within reports whether lines from..to all lie in one of ranges.
func within(ranges []LineRange, from, to int) bool {
	for _, r := range ranges {
		if r.From <= from && to <= r.To {
			return true
		}
	}
	return false
}

// hunk is one added hunk's code: its file, its first node and its shape.
type hunk struct {
	rel   string
	pkg   string
	at    syntax.Node
	shape string
}

func checkScatteredEdit(p *Pass) {
	if p.Change() == nil {
		return
	}
	groups := p.Memo(func() any { return scatteredHunks(p) }).(map[syntax.Node][]string)
	for _, d := range p.File.Decls {
		syntax.Inspect(d, func(n syntax.Node) bool {
			if pkgs, ok := groups[n]; ok {
				p.Report(n.Pos(), n.End(), fmt.Sprintf("this change adds the same code in %d packages: %s", len(pkgs), listText(pkgs)))
				return false
			}
			return true
		})
	}
}

// scatteredHunks maps the first node of each added hunk whose shape the
// change adds in scatteredMin or more packages to those packages, sorted.
func scatteredHunks(p *Pass) map[syntax.Node][]string {
	added := p.Change().Added
	var hunks []hunk
	for _, u := range p.Program() {
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			for _, r := range added[f.Rel] {
				nodes := nodesWithin(f.Syntax, r)
				if len(nodes) == 0 || f.Syntax.Line(nodes[len(nodes)-1].End())-f.Syntax.Line(nodes[0].Pos())+1 < scatteredLines {
					continue
				}
				hunks = append(hunks, hunk{rel: f.Rel, pkg: u.Package.Path, at: nodes[0], shape: shapeOfEdit(u.Package.Info, nodes...)})
			}
		}
	}
	byShape := map[string][]hunk{}
	for _, h := range hunks {
		byShape[h.shape] = append(byShape[h.shape], h)
	}
	out := map[syntax.Node][]string{}
	for _, g := range byShape {
		var pkgs []string
		for _, h := range g {
			pkgs = append(pkgs, h.pkg)
		}
		slices.Sort(pkgs)
		pkgs = slices.Compact(pkgs)
		if len(pkgs) < scatteredMin {
			continue
		}
		for _, h := range g {
			out[h.at] = pkgs
		}
	}
	return out
}

// nodesWithin are the outermost statements of file whose lines all lie in
// r, in source order: an edit inside a body. A declaration added whole is
// added-clone's to judge, so one finding has one remedy; its statements
// are not taken.
func nodesWithin(file *syntax.File, r LineRange) []syntax.Node {
	var out []syntax.Node
	for _, d := range file.Decls {
		syntax.Inspect(d, func(n syntax.Node) bool {
			in := r.From <= file.Line(n.Pos()) && file.Line(n.End()) <= r.To
			switch n.(type) {
			case syntax.Decl:
				return !in
			case syntax.Stmt:
				if in {
					out = append(out, n)
					return false
				}
			}
			return true
		})
	}
	return out
}
