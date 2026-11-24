package lint

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// longFile (layout, advisory): a checked source file whose physical line
// count exceeds the repository's configured maxFileLines. Physical line
// counts live in graphrows.Table.NodeLines (from syntax.File.LineCount)
// and are queried via std codegraph's PhysicalLines/LongFile through the
// linked `ergo graph files`.
var longFile = &Rule{
	Name:    "long-file",
	Code:    "L165",
	Doc:     "A checked source file of the linted module has more physical lines than the configured file max (option maxFileLines; none, no finding). Blank and comment lines count, an empty file has 0 lines, and a trailing newline does not add a line.",
	Instead: "split the file along concept boundaries rather than by line count",
	Default: Hint,
	Check:   checkLongFile,
	Options: options(func() longFileOptions { return longFileOptions{} }, func(o longFileOptions) error {
		if o.MaxFileLines < 0 {
			return fmt.Errorf("maxFileLines is %d; it is a positive number of lines, or absent for no finding", o.MaxFileLines)
		}
		return nil
	}),
}

type longFileOptions struct {
	MaxFileLines int `json:"maxFileLines"`
}

type fileLengthRow struct {
	FileId int
	File   string
	Lines  int
	Long   bool
}

func checkLongFile(p *Pass) {
	max := p.Options.(longFileOptions).MaxFileLines
	if max <= 0 {
		return
	}
	g := p.run.shared.programGraph()
	found, ok := p.Memo(func() any {
		raw, err := g.query(p.run.in.Units, "files", "--max", strconv.Itoa(max), "--json")
		if err != nil {
			return err
		}
		var rows []fileLengthRow
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		byFile := make(map[uint32]fileLengthRow, len(rows))
		for _, r := range rows {
			byFile[uint32(r.FileId)] = r
		}
		return byFile
	}).(map[uint32]fileLengthRow)
	if !ok {
		if p.bad == nil {
			p.bad = fmt.Errorf("long-file: %v", p.run.memo)
		}
		return
	}
	if row, hit := found[g.fileOf(p)]; hit {
		p.found = append(p.found, Finding{
			File:    p.rel,
			Offset:  0,
			Length:  1,
			Line:    1,
			Column:  1,
			Message: fmt.Sprintf("%s has %d lines (configured file max: %d)", row.File, row.Lines, max),
		})
	}
}
