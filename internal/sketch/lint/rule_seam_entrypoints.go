package lint

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketch/syntax"
	sdk "ergo.local/prototype/lint"
)

// seamEntrypoints (layout, advisory): an import enters a configured
// logical seam at a non-entry internal package, or (when
// testThroughEntries is set) a test inside the seam uses an internal
// package directly. Queried via std codegraph's SeamEntrypoints through
// the linked `ergo graph seams`.
var seamEntrypoints = &Rule{
	Name:    "seam-entrypoints",
	Code:    "L169",
	Doc:     "An import crosses into a configured logical seam (option seams; absent: inactive) at a non-entry package, or a test inside the seam uses an internal package when testThroughEntries is enabled.",
	Instead: "import an allowed entry package of the seam instead of its internal package",
	Default: Hint,
	Check:   checkSeamEntrypoints,
	Options: options(func() seamOptions { return seamOptions{} }, func(o seamOptions) error {
		for i, s := range o.Seams {
			if s.Root == "" && len(s.Packages) == 0 {
				return fmt.Errorf("seams[%d] needs root or packages", i)
			}
			for _, p := range s.Packages {
				if p == "" {
					return fmt.Errorf("seams[%d].packages holds an empty package path", i)
				}
			}
			if len(s.Entries) == 0 {
				return fmt.Errorf("seams[%d] needs at least one entry package", i)
			}
			for _, e := range s.Entries {
				if e == "" {
					return fmt.Errorf("seams[%d].entries holds an empty package path", i)
				}
				if !inSeamArea(e, s) {
					return fmt.Errorf("seams[%d]: entry %q is outside the seam area", i, e)
				}
			}
			for _, ts := range s.TestSupport {
				if ts == "" {
					return fmt.Errorf("seams[%d].testSupport holds an empty package path", i)
				}
				if !inSeamArea(ts, s) {
					return fmt.Errorf("seams[%d]: testSupport %q is outside the seam area", i, ts)
				}
			}
		}
		return nil
	}),
}

type seamConfig struct {
	Root               string   `json:"root"`
	Packages           []string `json:"packages"`
	Entries            []string `json:"entries"`
	TestSupport        []string `json:"testSupport"`
	TestThroughEntries bool     `json:"testThroughEntries"`
}

type seamOptions struct {
	Seams []seamConfig `json:"seams"`
}

func inSeamArea(pkg string, s seamConfig) bool {
	if slices.Contains(s.Packages, pkg) {
		return true
	}
	return s.Root != "" && (pkg == s.Root || strings.HasPrefix(pkg, s.Root+"/"))
}

type seamViolationRow struct {
	Seam      int
	Import    int
	File      int
	CallerPkg string
	TargetPkg string
	Inside    bool
	TestSites []int
}

type seamFinding struct {
	file    uint32
	off     syntax.Pos
	message string
	related []sdk.Related
}

func checkSeamEntrypoints(p *Pass) {
	opts := p.Options.(seamOptions)
	if len(opts.Seams) == 0 {
		return
	}
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		out, err := seamFindings(g, p.run.in.Units, opts)
		if err != nil {
			return err
		}
		return out
	}).([]seamFinding)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("seam-entrypoints: %v", p.run.memo)
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

func seamFindings(g *programGraph, units []Unit, opts seamOptions) ([]seamFinding, error) {
	args := []string{"seams", "--json"}
	for _, s := range opts.Seams {
		norm := seamConfig{
			Root:               s.Root,
			Packages:           nonNilStrings(s.Packages),
			Entries:            nonNilStrings(s.Entries),
			TestSupport:        nonNilStrings(s.TestSupport),
			TestThroughEntries: s.TestThroughEntries,
		}
		b, err := json.Marshal(norm)
		if err != nil {
			return nil, err
		}
		args = append(args, "--seam", string(b))
	}
	raw, err := g.query(units, args...)
	if err != nil {
		return nil, err
	}
	var rows []seamViolationRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	sorted := g.t.Sorted()
	var out []seamFinding
	for _, v := range rows {
		i := sorted[v.Import]
		at := g.t.EdgeAt[i]
		if at.File == graphrows.NoNode {
			continue
		}
		seam := opts.Seams[v.Seam]
		label := seam.Root
		if label == "" {
			label = seam.Entries[0]
		}
		entries := strings.Join(seam.Entries, " or ")
		var msg string
		if v.Inside {
			msg = fmt.Sprintf("test in %s enters %s at internal package %s; enter at %s", v.CallerPkg, label, v.TargetPkg, entries)
		} else {
			msg = fmt.Sprintf("%s enters %s at %s; enter at %s", v.CallerPkg, label, v.TargetPkg, entries)
		}
		var related []sdk.Related
		for _, ts := range v.TestSites {
			ti := sorted[ts]
			te, tat := g.t.Edges[ti], g.t.EdgeAt[ti]
			if tat.File == graphrows.NoNode {
				continue
			}
			related = append(related, sdk.Related{
				Message: g.fullName(te.From) + " uses " + g.fullName(te.To),
				Span:    sdk.Span{File: g.t.Str(g.t.Nodes[tat.File].Name), Offset: int(tat.Off), Length: 1, Line: int(tat.Line), Column: int(tat.Col)},
			})
		}
		out = append(out, seamFinding{file: at.File, off: syntax.Pos(at.Off), message: msg, related: related})
	}
	return out, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
