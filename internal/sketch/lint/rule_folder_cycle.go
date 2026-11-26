package lint

import (
	"encoding/json"
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// siblingFolderCycle (layout, advisory): two or more sibling folders
// under a common parent have mutually dependent declarations across
// packages in the same module. Queried via std codegraph's
// SiblingFolderCycles through the linked `ergo graph folder-cycles`.
var siblingFolderCycle = &Rule{
	Name:    "sibling-folder-cycle",
	Code:    "L170",
	Doc:     "Sibling folders under a common parent in the linted module have no dependency order: declarations in different packages under those folders depend on one another outside tests and laws.",
	Instead: "move shared policy into a dependency both folders can import, or compose them from a layer above",
	Default: Hint,
	Check:   checkSiblingFolderCycle,
}

type folderCycleRow struct {
	Parent  string
	Members []string
	Path    []string
	Sites   []int
}

type folderCycleFinding struct {
	file    uint32
	off     syntax.Pos
	message string
	related []sdk.Related
}

func checkSiblingFolderCycle(p *Pass) {
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		out, err := folderCycleFindings(g, p.run.in.Units)
		if err != nil {
			return err
		}
		return out
	}).([]folderCycleFinding)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("sibling-folder-cycle: %v", p.run.memo)
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

func folderCycleFindings(g *programGraph, units []Unit) ([]folderCycleFinding, error) {
	raw, err := g.query(units, "folder-cycles", "--json")
	if err != nil {
		return nil, err
	}
	var rows []folderCycleRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	sorted := g.sortedEdges()
	var out []folderCycleFinding
	for _, c := range rows {
		if len(c.Sites) == 0 {
			continue
		}
		first := sorted[c.Sites[0]]
		at := g.t.EdgeAt[first]
		if at.File == graphrows.NoNode {
			continue
		}
		var related []sdk.Related
		for _, s := range c.Sites {
			i := sorted[s]
			e, eat := g.t.Edges[i], g.t.EdgeAt[i]
			if eat.File == graphrows.NoNode {
				continue
			}
			related = append(related, sdk.Related{
				Message: g.fullName(e.From) + " -> " + g.fullName(e.To),
				Span:    sdk.Span{File: g.t.Str(g.t.Nodes[eat.File].Name), Offset: int(eat.Off), Length: 1, Line: int(eat.Line), Column: int(eat.Col)},
			})
		}
		msg := fmt.Sprintf("folders under %s have no dependency order: %s (%d folders)", c.Parent, strings.Join(c.Path, " -> "), len(c.Members))
		out = append(out, folderCycleFinding{file: at.File, off: syntax.Pos(at.Off), message: msg, related: related})
	}
	return out, nil
}
