package lint

import (
	"encoding/json"
	"fmt"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// foreignRecordFieldWrite (layout, advisory): outside updates of an
// exported field of a nominal record that also holds a private direct
// field. Queried via std codegraph's ForeignRecordFieldWrites through
// the linked `ergo graph field-writes`.
var foreignRecordFieldWrite = &Rule{
	Name:    "foreign-record-field-write",
	Code:    "L167",
	Doc:     "Declarations of another package of the linted module write (outside tests and laws) an exported direct field of a nominal record that also declares a private direct field.",
	Instead: "expose a behavioral update on the owning package where the invariant belongs, or make the record an all-public data type",
	Default: Hint,
	Check:   checkForeignRecordFieldWrite,
}

type fieldWriteRow struct {
	FieldId        int
	Field          string
	RecordId       int
	Record         string
	PrivateFieldId int
	PrivateField   string
	Callers        []string
	Sites          []int
}

type fieldWriteFinding struct {
	file    uint32
	off     syntax.Pos
	message string
	related []sdk.Related
}

func checkForeignRecordFieldWrite(p *Pass) {
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		out, err := foreignFieldWrites(g, p.run.in.Units)
		if err != nil {
			return err
		}
		return out
	}).([]fieldWriteFinding)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("foreign-record-field-write: %v", p.run.memo)
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

func foreignFieldWrites(g *programGraph, units []Unit) ([]fieldWriteFinding, error) {
	raw, err := g.query(units, "field-writes", "--json")
	if err != nil {
		return nil, err
	}
	var rows []fieldWriteRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	sorted := g.sortedEdges()
	var out []fieldWriteFinding
	for _, w := range rows {
		field := uint32(w.FieldId)
		at := g.t.NodeAt[field]
		if at.File == graphrows.NoNode {
			return nil, fmt.Errorf("foreign-record-field-write: %s has no position", w.Field)
		}
		callerPkgs := map[uint32]bool{}
		var related []sdk.Related
		for _, s := range w.Sites {
			i := sorted[s]
			e, eat := g.t.Edges[i], g.t.EdgeAt[i]
			if pkg := g.pkg[e.From]; pkg != graphrows.NoNode {
				callerPkgs[pkg] = true
			}
			if eat.File == graphrows.NoNode {
				continue
			}
			related = append(related, sdk.Related{
				Message: g.fullName(e.From) + " writes " + g.fullName(e.To),
				Span:    sdk.Span{File: g.t.Str(g.t.Nodes[eat.File].Name), Offset: int(eat.Off), Length: 1, Line: int(eat.Line), Column: int(eat.Col)},
			})
		}
		if w.PrivateFieldId >= 0 {
			pat := g.t.NodeAt[uint32(w.PrivateFieldId)]
			if pat.File != graphrows.NoNode {
				related = append(related, sdk.Related{
					Message: w.Record + " has private field " + w.PrivateField,
					Span:    sdk.Span{File: g.t.Str(g.t.Nodes[pat.File].Name), Offset: int(pat.Off), Length: 1, Line: int(pat.Line), Column: int(pat.Col)},
				})
			}
		}
		callerWord := "callers"
		if len(w.Callers) == 1 {
			callerWord = "caller"
		}
		pkgWord := "packages"
		if len(callerPkgs) == 1 {
			pkgWord = "package"
		}
		msg := fmt.Sprintf("outside updates of %s reach a representation field of a record with a private field (%d %s in %d %s)", w.Field, len(w.Callers), callerWord, len(callerPkgs), pkgWord)
		out = append(out, fieldWriteFinding{file: at.File, off: syntax.Pos(at.Off), message: msg, related: related})
	}
	return out, nil
}
