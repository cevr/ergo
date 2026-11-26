package lint

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// declarationTangle (layout, advisory): a strongly connected component
// of two or more top-level declarations within one package that spans
// multiple files (or exceeds configured maxTangleMembers). Queried via
// std codegraph's DeclarationTanglesOf through the linked `ergo graph
// tangles`.
var declarationTangle = &Rule{
	Name:    "declaration-tangle",
	Code:    "L171",
	Doc:     "Two or more top-level declarations of one package of the linted module depend on one another outside tests and laws across more than one file (or exceed option maxTangleMembers when configured; absent: cross-file placement arm only).",
	Instead: "co-locate inseparable declarations in one file, or extract a one-way mediator for distinct responsibilities",
	Default: Hint,
	Check:   checkDeclarationTangle,
	Options: options(func() tangleOptions { return tangleOptions{} }, func(o tangleOptions) error {
		if o.MaxTangleMembers < 0 {
			return fmt.Errorf("maxTangleMembers is %d; it is a positive number of declarations, or absent for cross-file tangles only", o.MaxTangleMembers)
		}
		return nil
	}),
}

type tangleOptions struct {
	MaxTangleMembers int `json:"maxTangleMembers"`
}

type tangleRow struct {
	Package   string
	Members   []string
	Files     []string
	Path      []string
	Sites     []int
	CrossFile bool
}

type tangleFinding struct {
	file    uint32
	off     syntax.Pos
	message string
	related []sdk.Related
}

func checkDeclarationTangle(p *Pass) {
	max := p.Options.(tangleOptions).MaxTangleMembers
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		out, err := tangleFindings(g, p.run.in.Units, max)
		if err != nil {
			return err
		}
		return out
	}).([]tangleFinding)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("declaration-tangle: %v", p.run.memo)
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

func tangleFindings(g *programGraph, units []Unit, max int) ([]tangleFinding, error) {
	raw, err := g.query(units, "tangles", "--max", strconv.Itoa(max), "--json")
	if err != nil {
		return nil, err
	}
	var rows []tangleRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	sorted := g.sortedEdges()
	var out []tangleFinding
	for _, t := range rows {
		if len(t.Sites) == 0 {
			continue
		}
		first := sorted[t.Sites[0]]
		at := g.t.EdgeAt[first]
		if at.File == graphrows.NoNode {
			continue
		}
		var related []sdk.Related
		for _, s := range t.Sites {
			i := sorted[s]
			e, eat := g.t.Edges[i], g.t.EdgeAt[i]
			if eat.File == graphrows.NoNode {
				continue
			}
			related = append(related, sdk.Related{
				Message: fmt.Sprintf("%s -> %s (%s)", g.fullName(e.From), g.fullName(e.To), e.How),
				Span:    sdk.Span{File: g.t.Str(g.t.Nodes[eat.File].Name), Offset: int(eat.Off), Length: 1, Line: int(eat.Line), Column: int(eat.Col)},
			})
		}
		var msg string
		if t.CrossFile {
			msg = fmt.Sprintf("%d declarations in %s are mutually dependent across %d files: %s", len(t.Members), t.Package, len(t.Files), strings.Join(t.Path, " -> "))
		} else {
			msg = fmt.Sprintf("%d declarations in %s are mutually dependent (configured tangle max: %d): %s", len(t.Members), t.Package, max, strings.Join(t.Path, " -> "))
		}
		out = append(out, tangleFinding{file: at.File, off: syntax.Pos(at.Off), message: msg, related: related})
	}
	return out, nil
}
