package lint

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// wideDirectory (layout, advisory): a directory whose immediate checked
// source files and child directories containing checked source exceed
// the repository's configured maxDirectoryMembers. Queried via std
// codegraph's Directories/WideDirectory through the linked `ergo graph
// directories`.
var wideDirectory = &Rule{
	Name:    "wide-directory",
	Code:    "L166",
	Doc:     "A directory of the linted module holds more immediate checked source files and child directories containing checked source than the configured directory max (option maxDirectoryMembers; none, no finding).",
	Instead: "split the directory into coherent concept subpackages rather than moving files arbitrarily",
	Default: Hint,
	Check:   checkWideDirectory,
	Options: options(func() wideDirOptions { return wideDirOptions{} }, func(o wideDirOptions) error {
		if o.MaxDirectoryMembers < 0 {
			return fmt.Errorf("maxDirectoryMembers is %d; it is a positive number of entries, or absent for no finding", o.MaxDirectoryMembers)
		}
		return nil
	}),
}

type wideDirOptions struct {
	MaxDirectoryMembers int `json:"maxDirectoryMembers"`
}

type directoryRow struct {
	Path    string
	Members []string
	Anchor  int
	Wide    bool
}

func checkWideDirectory(p *Pass) {
	max := p.Options.(wideDirOptions).MaxDirectoryMembers
	if max <= 0 {
		return
	}
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		raw, err := g.query(p.run.in.Units, "directories", "--max", strconv.Itoa(max), "--json")
		if err != nil {
			return err
		}
		var rows []directoryRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		byFile := map[uint32][]directoryRow{}
		for _, r := range rows {
			if r.Anchor >= 0 {
				byFile[uint32(r.Anchor)] = append(byFile[uint32(r.Anchor)], r)
			}
		}
		return byFile
	}).(map[uint32][]directoryRow)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("wide-directory: %v", p.run.memo)
		}
		return
	}
	for _, d := range found[g.fileOf(p)] {
		preview := strings.Join(d.Members, ", ")
		if len(d.Members) > 5 {
			preview = fmt.Sprintf("%s, and %d more", strings.Join(d.Members[:5], ", "), len(d.Members)-5)
		}
		p.found = append(p.found, Finding{
			File:    p.rel,
			Offset:  0,
			Length:  1,
			Line:    1,
			Column:  1,
			Message: fmt.Sprintf("%s/ holds %d source entries (configured directory max: %d): %s", d.Path, len(d.Members), max, preview),
		})
	}
}
