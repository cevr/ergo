package lint

import (
	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
)

// programGraph is the graph rows of a run's whole program, built once
// per run on first use (runShared.programGraph) and read by every graph
// rule. They are the rows `ergo graph export` writes for the same
// packages (sketchfront.GraphOf), with no tasks, since no rule reads
// one: the required modules' packages too, each package under its
// module, so a module-relative file name never stands for another
// module's file. The indexes are dense columns by node id and file
// index, each built in one pass over the rows.
type programGraph struct {
	t *graphrows.Table
	// pkg and file are each node's package and file node, NoNode for
	// none.
	pkg, file []uint32
	// subject marks the package nodes of the linted module's own
	// packages (Unit.Dependency false): the packages a rule reports on.
	// A required module's rows are evidence only.
	subject []bool
	// files is the file node of each file of each unit:
	// Input.Units[u].Files[i] is files[first[u]+i]. Build adds a file node
	// for each file of each package in order, so the k-th file node of
	// the rows is the k-th file of the units.
	files []uint32
	first []int
	// fileIndex is the index in files of each file node, -1 for any
	// other node.
	fileIndex []int32
	// at lists, by file index, the edges positioned in that file; into,
	// the uses edges whose target sits in that file; both in row order.
	at, into rowLists
}

// rowLists is a list of edge rows for each of a run of keys, in one
// array (compressed rows): the rows of key k are edges[start[k]:start[k+1]].
type rowLists struct {
	start []int32
	edges []int32
}

// of is the rows of key k.
func (l rowLists) of(k int) []int32 { return l.edges[l.start[k]:l.start[k+1]] }

// rowListsOf is the rows of t grouped by key, n keys, each in row
// order; a row whose key is -1 is in no list.
func rowListsOf(t *graphrows.Table, n int, key func(i int) int) rowLists {
	l := rowLists{start: make([]int32, n+1)}
	for i := range t.Edges {
		if k := key(i); k >= 0 {
			l.start[k+1]++
		}
	}
	for k := range n {
		l.start[k+1] += l.start[k]
	}
	l.edges = make([]int32, l.start[n])
	next := append([]int32(nil), l.start[:n]...)
	for i := range t.Edges {
		if k := key(i); k >= 0 {
			l.edges[next[k]] = int32(i)
			next[k]++
		}
	}
	return l
}

// programGraph is the run's graph, built on first use.
func (s *runShared) programGraph() *programGraph {
	if s.graph == nil {
		s.graph = programGraphOf(s.in.Units)
	}
	return s.graph
}

// programGraphOf builds the rows of units, dependencies first, and their
// indexes.
func programGraphOf(units []Unit) *programGraph {
	pkgs := make([]graphrows.SourcePackage, len(units))
	g := &programGraph{first: make([]int, len(units)+1)}
	modules := make(map[string]string, len(units))
	for _, unit := range units {
		modules[unitPath(unit)] = unit.Module
	}
	for u, unit := range units {
		sp := graphrows.SourcePackage{Path: unitPath(unit), Module: unit.Module, Modules: modules, Info: unit.Package}
		for _, f := range unit.Files {
			sp.Files = append(sp.Files, graphrows.SourceFile{Name: f.Rel, Syntax: f.Syntax})
		}
		pkgs[u] = sp
		g.first[u+1] = g.first[u] + len(unit.Files)
	}
	t := graphrows.Build(pkgs)
	g.t = t
	g.pkg = make([]uint32, len(t.Nodes))
	g.file = make([]uint32, len(t.Nodes))
	g.fileIndex = make([]int32, len(t.Nodes))
	g.subject = make([]bool, len(t.Nodes))
	for i, n := range t.Nodes {
		g.pkg[i], g.file[i], g.fileIndex[i] = graphrows.NoNode, graphrows.NoNode, -1
		switch n.Kind {
		case graphrows.Package:
			g.pkg[i] = uint32(i)
		case graphrows.File:
			g.fileIndex[i] = int32(len(g.files))
			g.files = append(g.files, uint32(i))
			g.file[i], g.pkg[i] = uint32(i), n.Parent
		default:
			// A node's parent comes before it: a file's or a
			// declaration's holder is already placed.
			if p := n.Parent; p != graphrows.NoNode && t.Nodes[p].Kind != graphrows.Module {
				g.pkg[i], g.file[i] = g.pkg[p], g.file[p]
			}
		}
	}
	if len(g.files) != g.first[len(units)] {
		panic("lint: the graph rows hold another count of files than the program")
	}
	for u, unit := range units {
		if !unit.Dependency {
			for _, f := range g.files[g.first[u]:g.first[u+1]] {
				g.subject[g.pkg[f]] = true
			}
		}
	}
	g.at = rowListsOf(t, len(g.files), func(i int) int {
		if f := t.EdgeAt[i].File; f != graphrows.NoNode {
			return int(g.fileIndex[f])
		}
		return -1
	})
	g.into = rowListsOf(t, len(g.files), func(i int) int {
		if e := t.Edges[i]; e.Kind == graphrows.Uses && g.file[e.To] != graphrows.NoNode {
			return int(g.fileIndex[g.file[e.To]])
		}
		return -1
	})
	return g
}

// fileOf is the file node of p's file.
func (g *programGraph) fileOf(p *Pass) uint32 { return g.files[g.first[p.unit]+p.index] }

// edgesIn calls visit with each edge of kind positioned in file node f,
// in row order, and its offset there.
func (g *programGraph) edgesIn(f uint32, kind graphrows.EdgeKind, visit func(e graphrows.Edge, off syntax.Pos)) {
	for _, i := range g.at.of(int(g.fileIndex[f])) {
		if e := g.t.Edges[i]; e.Kind == kind {
			visit(e, syntax.Pos(g.t.EdgeAt[i].Off))
		}
	}
}
