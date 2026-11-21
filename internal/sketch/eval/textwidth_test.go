package eval

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestTextCellsIsBun is the property WidthIsTheTable (stdlib.md, Text
// width): std text.Cells gives Bun 1.4.4's Bun.stringWidth on every row of
// testdata/textwidth/cells.jsonl, which testdata/textwidth/record.ts
// generates and records once: 3000 texts of whole clusters (ASCII words,
// CJK, letters with combining marks, emoji, skin tones, ZWJ sequences,
// flags, U+FE0F forms, keycaps, controls, SGR, CSI and OSC 8 escapes),
// then each end of every range of the recorded tables, alone. A failing
// row prints the text and both widths.
//
// Mutants only it kills (internal/sketch/stdlib/testdata/mutants.json):
// sq2-zwj-not-joined, sq2-vs16-ignored, sq2-tone-counted,
// sq2-flag-unpaired, sq2-keycap-narrow, sq2-osc-unended,
// sq2-range-last-pair, sq2-wide-is-one.
func TestTextCellsIsBun(t *testing.T) {
	pkg := checked(t, "import text\n\nfunc cells(s string) int { text.Cells(s) }\n\ntest T {\n\tassert true\n}\n")
	var cells *check.Func
	for _, f := range pkg.Funcs {
		if f.Name == "cells" {
			cells = f
		}
	}
	in := &interp{budget: DefaultBudget(), root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, host: &host{}}
	if f := in.initialize(pkg); f != nil {
		t.Fatalf("initializers: %s", f.reason)
	}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	f, err := os.Open("testdata/textwidth/cells.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, bad := 0, 0
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		var row []any
		if err := json.Unmarshal(lines.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		text, want := row[1].(string), int64(row[2].(float64))
		in.steps, in.bytes = 0, 0
		v, ab := in.call(fr, nil, cells, nil, []Value{text})
		if ab != nil {
			t.Fatalf("Cells(%q): %v", text, ab)
		}
		if got := v.(int64); got != want {
			if bad++; bad <= 10 {
				t.Errorf("Cells(%q) = %d, Bun.stringWidth %d (%v)", text, got, want, row[3:])
			}
		}
		rows++
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if rows < 3000 || bad > 0 {
		t.Errorf("%d of %d rows differ", bad, rows)
	}
}
