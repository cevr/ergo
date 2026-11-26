package lint

import (
	"encoding/json"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// productionUsesTestSupport (layout, advisory): an ordinary declaration
// outside configured test support uses a declaration of a designated
// test-support package. Queried via std codegraph's
// ProductionUsesTestSupport through the linked `ergo graph test-support`.
var productionUsesTestSupport = &Rule{
	Name:    "production-uses-test-support",
	Code:    "L168",
	Doc:     "An ordinary declaration outside configured test support (options packages and seams; absent: inactive) uses a declaration of a designated test-support package; tests and laws may use test support, and support packages within one named seam may use one another.",
	Instead: "move the fixture use into a test or law, or pass a capability provider bound at entry",
	Default: Hint,
	Check:   checkProductionUsesTestSupport,
	Options: options(func() testSupportOptions { return testSupportOptions{} }, func(o testSupportOptions) error {
		for _, p := range o.Packages {
			if p == "" {
				return fmt.Errorf("packages holds an empty package path")
			}
		}
		for seam, pkgs := range o.Seams {
			if seam == "" {
				return fmt.Errorf("seams holds an empty seam name")
			}
			for _, p := range pkgs {
				if p == "" {
					return fmt.Errorf("seams[%q] holds an empty package path", seam)
				}
			}
		}
		return nil
	}),
}

type testSupportOptions struct {
	Packages []string            `json:"packages"`
	Seams    map[string][]string `json:"seams"`
}

type testSupportRow struct {
	CallerId int
	Caller   string
	TargetId int
	Target   string
	Site     int
	Import   int
}

type testSupportFinding struct {
	file    uint32
	off     syntax.Pos
	message string
	related []sdk.Related
}

func checkProductionUsesTestSupport(p *Pass) {
	opts := p.Options.(testSupportOptions)
	if len(opts.Packages) == 0 && len(opts.Seams) == 0 {
		return
	}
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		out, err := testSupportFindings(g, p.run.in.Units, opts)
		if err != nil {
			return err
		}
		return out
	}).([]testSupportFinding)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("production-uses-test-support: %v", p.run.memo)
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

func testSupportFindings(g *programGraph, units []Unit, opts testSupportOptions) ([]testSupportFinding, error) {
	args := []string{"test-support", "--json"}
	for _, pkg := range opts.Packages {
		args = append(args, "--support", pkg)
	}
	var seamNames []string
	for name := range opts.Seams {
		seamNames = append(seamNames, name)
	}
	slices.Sort(seamNames)
	for _, name := range seamNames {
		for _, pkg := range opts.Seams[name] {
			args = append(args, "--support-seam", name+"="+pkg)
		}
	}
	raw, err := g.query(units, args...)
	if err != nil {
		return nil, err
	}
	var rows []testSupportRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	sorted := g.sortedEdges()
	var out []testSupportFinding
	for _, u := range rows {
		i := sorted[u.Site]
		e, at := g.t.Edges[i], g.t.EdgeAt[i]
		if at.File == graphrows.NoNode {
			continue
		}
		var related []sdk.Related
		if u.Import >= 0 {
			ii := sorted[u.Import]
			iat := g.t.EdgeAt[ii]
			if iat.File != graphrows.NoNode {
				related = append(related, sdk.Related{
					Message: "imports configured test support " + u.Target,
					Span:    sdk.Span{File: g.t.Str(g.t.Nodes[iat.File].Name), Offset: int(iat.Off), Length: 1, Line: int(iat.Line), Column: int(iat.Col)},
				})
			}
		}
		msg := fmt.Sprintf("%s uses %s from configured test support %s", u.Caller, g.fullName(e.To), u.Target)
		out = append(out, testSupportFinding{file: at.File, off: syntax.Pos(at.Off), message: msg, related: related})
	}
	return out, nil
}
