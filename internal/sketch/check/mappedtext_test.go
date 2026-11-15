package check

import (
	"strings"
	"testing"
)

// TestMappedTextRows pins the text and position of each Borrows 8
// diagnostic that TestMappedTextModel does not reach: a written fs.Text
// outside a parameter's type, a text with no name in a refused place, a
// conversion to fs.Text, the text of a
// temporary Mapped, a Mapped that a let mut holds, a text that a let mut
// holds, a call passed m's text that takes m, and a local closure that
// captures a text and runs after Close. A row's lines count from its own
// first line; a position inside a message stays the file's.
// Mutants (check ledger): mapped-text-placement-open (textPlacement
// accepts every place), mapped-text-conversion-open (a conversion to
// fs.Text is accepted), mapped-text-temporary-open (textTemporary finds
// none), mapped-text-mut-owner-open (textOwner accepts a let mut),
// mapped-text-mut-let-open (a let mut holds a text), mapped-text-loan-open
// (textLoan lends nothing), mapped-text-capture-open (a captured text
// lends nothing to its closure).
func TestMappedTextRows(t *testing.T) {
	const prelude = "import fs\n\nfunc count(t fs.Text) int { len(t) }\n\n"
	line := strings.Count(prelude, "\n")
	rows := []struct {
		name, src string
		want      []string
	}{
		{"result", "func keep(t fs.Text) fs.Text { t }", []string{
			"1:22: S108 fs.Text is a borrow of its Mapped and stands only as a parameter's type, never as a result, a field, an element, a type argument or a let's type (types.md, Borrows 8); take the text as a parameter, or copy it with fs.Clone",
			"1:32: S108 t is a mapped text (fs.Text), a borrow of its Mapped: it stands only as a let's value, an argument of an fs.Text parameter, the operand of an index, a slice, len or a comparison, or the range of a for (types.md, Borrows 8); copy it with fs.Clone to keep it",
		}},
		{"assigned", "func keep(t fs.Text) int {\n\tlet mut s = \"\"\n\ts = \"\\(t[0:1])\"\n\tlen(s)\n}", []string{
			"3:9: S108 this text is a mapped text (fs.Text), a borrow of its Mapped: it stands only as a let's value, an argument of an fs.Text parameter, the operand of an index, a slice, len or a comparison, or the range of a for (types.md, Borrows 8); copy it with fs.Clone to keep it",
		}},
		{"field", "type Box { t fs.Text }", []string{
			"1:14: S108 fs.Text is a borrow of its Mapped and stands only as a parameter's type, never as a result, a field, an element, a type argument or a let's type (types.md, Borrows 8); take the text as a parameter, or copy it with fs.Clone",
		}},
		{"conversion", "func conv() int { count(fs.Text(\"abc\")) }", []string{
			"1:25: S108 fs.Text converts to and from nothing: a text is a borrow of its Mapped (m.Text()), and fs.Clone copies one into a string (types.md, Borrows 8)",
		}},
		{"temporary", "func temp() int {\n\tlet t = fs.MapText(\"abc\").Text()\n\tlen(t)\n}", []string{
			"2:10: S108 the Mapped of this text is a temporary, released at the end of its statement, and the let would keep its text (types.md, Borrows 8); bind the Mapped with let, then its text",
		}},
		{"mut-owner", "func mutOwner() int {\n\tlet mut m = fs.MapText(\"abc\")\n\tlet t = m.Text()\n\tm = fs.MapText(\"x\")\n\tlen(t)\n}", []string{
			"3:10: S108 m is a let mut or a borrow, and an assignment to it would release the Mapped its text borrows (types.md, Borrows 8); read the text of a Mapped that a let or a parameter holds",
		}},
		{"mut-text", "func mutText() int {\n\tlet m = fs.MapText(\"abc\")\n\tlet mut t = m.Text()\n\tlen(t)\n}", []string{
			"3:10: S108 t is a mapped text (fs.Text), a borrow of its Mapped, so a let mut cannot hold it: one let holds one text (types.md, Borrows 8); bind it with let",
			"3:10: S110 t is never reassigned; write let t (#196, 196v)",
		}},
		{"same-call", "func taker(t fs.Text, m fs.Mapped) int takes m {\n\tm.Close()\n\tlen(t)\n}\n\nfunc sameCall() int {\n\tlet m = fs.MapText(\"abc\")\n\ttaker(m.Text(), m)\n}", []string{
			"8:8: S108 m is lent to taker here and handed off at 12:18 (taker takes m) in the same call (resources R17); hand it off in a statement of its own",
		}},
		{"captured", "func localFrame() int {\n\tlet m = fs.MapText(\"abc\")\n\tlet t = m.Text()\n\tlet f = func() int { len(t) }\n\tm.Close()\n\tf()\n}", []string{
			"5:2: S108 m is lent to a task or closure that may still run (resources R28, R32); stop or wait for its keeper before taking or replacing it",
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, diags := CheckSource(prelude + row.src + "\n\nfunc main() {}\n")
			var got []string
			for _, d := range diags {
				d.Line -= line
				got = append(got, d.String())
			}
			if strings.Join(got, "\n") != strings.Join(row.want, "\n") {
				t.Errorf("got\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(row.want, "\n\t"))
			}
		})
	}
}
