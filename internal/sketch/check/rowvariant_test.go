package check_test

import (
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRowVariantCases holds a variant whose field carries the union's row
// parameter, matched in a function generic over that row (ROW-PARAM AC1,
// AC2; UC's probe, lang/ROW-PARAM/uc-probe.md): a case written with the
// variant's arguments (Generics 6 and 7) or bare takes the subject's
// variant, so the field's call fails the function's own row. Each row is
// the smallest program for one rule, with the one diagnostic it gets, or
// none; no S101 suggestion repeats a member the row has (UC's no-op
// `fails F | F`). Mutants it alone catches (testdata/mutants.json):
// case-variant-left-out-row, bare-variant-keeps-own-row.
func TestRowVariantCases(t *testing.T) {
	const job = "type Idle {}\n\ntype Job[F] = Idle | Run{Go func() int fails F}\n\n"
	const async2 = "type Waiting {}\n\ntype Async2[T, F] = Waiting | Retrying{Again func() T fails F} | Failed{Error F}\n\n"
	for _, row := range []struct {
		name, src string
		want      string // "CODE line:column message prefix", or "" for none
	}{
		{"AC1 a written variant takes the subject's row", job + "func Do[F](j Job[F]) int fails F {\n\tmatch j {\n\tcase Idle:\n\t\t0\n\tcase Run[F] as r:\n\t\ttry r.Go()\n\t}\n}\n\nfunc main() {}\n", ""},
		{"AC1 two parameters, the row written", async2 + "func Again[T, F](a Async2[T, F]) T fails F | Waiting {\n\tmatch a {\n\tcase Waiting as w:\n\t\treturn w\n\tcase Retrying[T, F] as r:\n\t\treturn r.Again()\n\tcase Failed[F] as x:\n\t\treturn x.Error\n\t}\n}\n\nfunc main() {}\n", ""},
		{"AC1 two parameters, the row left out", async2 + "func Again[T, F](a Async2[T, F]) T fails F | Waiting {\n\tmatch a {\n\tcase Waiting as w:\n\t\treturn w\n\tcase Retrying[T] as r:\n\t\treturn r.Again()\n\tcase Failed[F] as x:\n\t\treturn x.Error\n\t}\n}\n\nfunc main() {}\n", ""},
		{"AC2 a bare variant takes the subject's row", job + "func Do[F](j Job[F]) int fails F {\n\tmatch j {\n\tcase Idle:\n\t\t0\n\tcase Run as r:\n\t\ttry r.Go()\n\t}\n}\n\nfunc main() {}\n", ""},
		{"AC2 an instance: the field fails the instance's row", job + "type Boom { at int }\n\nfunc main() {\n\tlet j Job[Boom] = Run{Go: func() int fails Boom { 7 }}\n\tlet v = match j {\n\tcase Idle:\n\t\t0\n\tcase Run as r:\n\t\tr.Go() catch {\n\t\tcase Boom:\n\t\t\t-1\n\t\t}\n\t}\n\tlet _ = v\n}\n", ""},
		{"the escape still names a missing member", job + "type Other {}\n\nfunc Do[F](j Job[F]) int fails Other {\n\tmatch j {\n\tcase Idle:\n\t\t0\n\tcase Run as r:\n\t\ttry r.Go()\n\t}\n}\n\nfunc main() {}\n", "S101 "},
	} {
		_, ds := check.CheckSource(row.src)
		var got []string
		for _, d := range ds {
			got = append(got, d.Code+" "+strconv.Itoa(d.Line)+":"+strconv.Itoa(d.Column)+" "+d.Message)
		}
		switch {
		case row.want == "" && len(got) > 0:
			t.Errorf("%s: want no diagnostic, got %q\n%s", row.name, got, row.src)
		case row.want != "" && (len(got) != 1 || !strings.HasPrefix(got[0], row.want)):
			t.Errorf("%s: want one diagnostic %q…, got %q\n%s", row.name, row.want, got, row.src)
		}
		for _, g := range got {
			// A suggestion never names a member the row already has
			// (escape-suggests-present-member): "fails F | F" is a no-op.
			if i := strings.Index(g, "write: "); i >= 0 {
				if r := g[i:]; strings.Contains(r, "fails F | F") || strings.Contains(r, "| F | F") {
					t.Errorf("%s: the suggestion repeats a member: %q", row.name, g)
				}
			}
		}
	}
}
