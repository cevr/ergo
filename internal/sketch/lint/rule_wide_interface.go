package lint

import (
	"encoding/json"
	"fmt"
	"strconv"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// wideObservedInterface (layout, advisory): the clients of a package in
// this check use more callable entries of it than the repository's list
// max, and its longest list exceeds the max too. The query is std
// codegraph's ObservedInterfaces through the linked `ergo graph
// interfaces` (Wide); the rule only places what it answers.
var wideObservedInterface = &Rule{
	Name:    "wide-observed-interface",
	Code:    "L164",
	Doc:     "Declarations of other packages of the module call or read more callable entries (funcs and operation members, through re-exporting lets too) of a package than the configured list max (option maxSurfaceList; none, no finding), outside tests and laws, and its longest list is longer than the max too: the number of lists, or the entries of one list (an operation type's members, or the funcs that use one type of the package). The private functions the entries reach are evidence only: they never clear a finding.",
	Instead: "put the entries clients use together behind one operation with a real contract, or split the package by responsibility",
	Default: Hint,
	Check:   checkWideObservedInterface,
	Options: options(func() surfaceOptions { return surfaceOptions{} }, func(o surfaceOptions) error {
		if o.MaxSurfaceList < 0 {
			return fmt.Errorf("maxSurfaceList is %d; it is a positive number of entries, or absent for no finding", o.MaxSurfaceList)
		}
		return nil
	}),
}

// surfaceOptions are wide-observed-interface's options: the longest list
// of entries the repository admits, 0 for no budget.
type surfaceOptions struct {
	MaxSurfaceList int `json:"maxSurfaceList"`
}

// observedInterface is one package of `ergo graph interfaces --json`.
type observedInterface struct {
	PackageId int
	Package   string
	Entries   []struct {
		Id   int
		Name string
		Site int
	}
	Groups []struct {
		Id      int
		Name    string
		Entries []int
	}
	Widest     int
	Wide       bool
	Backing    []string
	Unresolved int
}

// wideFinding is a finding placed at its anchor's file node.
type wideFinding struct {
	file    uint32
	off     syntax.Pos
	message string
	related []sdk.Related
}

func checkWideObservedInterface(p *Pass) {
	max := p.Options.(surfaceOptions).MaxSurfaceList
	if max <= 0 {
		return
	}
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		out, err := wideInterfaces(g, p.run.in.Units, max)
		if err != nil {
			return err
		}
		return out
	}).([]wideFinding)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("wide-observed-interface: %v", p.run.memo)
		}
		return
	}
	file := g.fileOf(p)
	for _, f := range found {
		if f.file == file {
			line, column := p.File.Position(f.off)
			p.found = append(p.found, Finding{File: p.rel, Offset: int(f.off), Length: 1, Line: line, Column: column, Message: f.message, Related: f.related})
		}
	}
}

// interfacesOf is `ergo graph interfaces --max max --json` over g's rows,
// for the modules of units' own packages.
func interfacesOf(g *programGraph, units []Unit, max int) ([]observedInterface, error) {
	raw, err := g.query(units, "interfaces", "--max", strconv.Itoa(max), "--json")
	if err != nil {
		return nil, err
	}
	var got []observedInterface
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil, err
	}
	return got, nil
}

// wideInterfaces is each wide interface of g, placed at its longest
// list's node: the type its entries use, or its first entry.
func wideInterfaces(g *programGraph, units []Unit, max int) ([]wideFinding, error) {
	got, err := interfacesOf(g, units, max)
	if err != nil {
		return nil, err
	}
	sorted := g.sortedEdges()
	var out []wideFinding
	for _, w := range got {
		site := map[int]int{}
		for _, e := range w.Entries {
			site[e.Id] = e.Site
		}
		largest := 0
		for i, gr := range w.Groups {
			if len(gr.Entries) > len(w.Groups[largest].Entries) {
				largest = i
			}
		}
		list := w.Groups[largest]
		anchor := uint32(list.Id)
		message := fmt.Sprintf("clients in this check use %d callable entries of %s; %s has %d entries (configured list max: %d)", len(w.Entries), w.Package, list.Name, len(list.Entries), max)
		if len(w.Groups) > len(list.Entries) {
			anchor = uint32(w.Entries[0].Id)
			message = fmt.Sprintf("clients in this check use %d callable entries of %s, in %d lists (configured list max: %d)", len(w.Entries), w.Package, len(w.Groups), max)
		}
		message += fmt.Sprintf("; %d private functions reached, %d entries with no func behind them", len(w.Backing), w.Unresolved)
		var related []sdk.Related
		for _, id := range list.Entries {
			i := sorted[site[id]]
			e, at := g.t.Edges[i], g.t.EdgeAt[i]
			if at.File == graphrows.NoNode {
				continue
			}
			related = append(related, sdk.Related{
				Message: g.fullName(e.From) + " uses " + g.fullName(e.To),
				Span:    sdk.Span{File: g.t.Str(g.t.Nodes[at.File].Name), Offset: int(at.Off), Length: 1, Line: int(at.Line), Column: int(at.Col)},
			})
		}
		at := g.t.NodeAt[anchor]
		if at.File == graphrows.NoNode {
			return nil, fmt.Errorf("wide-observed-interface: %s has no position", g.fullName(anchor))
		}
		out = append(out, wideFinding{file: at.File, off: syntax.Pos(at.Off), message: message, related: related})
	}
	return out, nil
}

// fullName is a declaration's package path, a dot and its name.
func (g *programGraph) fullName(n uint32) string {
	name := g.t.Str(g.t.Nodes[n].Name)
	if p := g.pkg[n]; p != graphrows.NoNode && g.t.Nodes[n].Kind == graphrows.Decl {
		return g.t.Str(g.t.Nodes[p].Name) + "." + name
	}
	return name
}
