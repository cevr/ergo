package check

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// ofDeclaredHandles extends TestOwnershipFlowModel's grammar with a
// declared root that holds handles (EXH1; resources R17, R39; audit CSX
// §4.8 P3). Board implements Closer and holds a sync.Ref, a slice of
// them, an Option of one, a plain record holding one, and an int. Each
// seed writes functions over a lent, a taken or a borrowed Board. Each
// function reads one of the board's parts (a field, an element by index, a
// nested field, the binding of a for-in or of a match arm, each also
// through a let) and then calls it, passes it on, returns it, stores it in
// a literal of a type that implements Closer, of a plain record or of a
// slice and returns that, or hands it to a taking call (Append); or it
// takes the board and updates it, or writes the borrowed board in place.
//
// The reference is the trace model: a value read from the board is the
// board's, so it may be used while the board lives; it is a second owner
// when it outlives the function (returned, or stored in what is
// returned), since the board's release (the caller's, or this function's
// for a taken board) reaches every copy. An int part is plain data. A
// record update moves the board whole, which a lent board cannot give. A
// write back to the board's own field leaves one owner. The checker must
// refuse exactly the functions the model finds a second owner in, at the
// line where the part leaves.
//
// Mutants it kills that no other test does (ledger: internal/sketch/check):
// exh1-append-escape, exh1-let-alias, exh1-in-place-refused,
// exh1-option-unseen, exh1-plain-as-handle, exh1-update-lent.
func ofDeclaredHandles(t *testing.T, seed uint64) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 0xe4a1))
	var b strings.Builder
	b.WriteString(dhPrelude)
	line := strings.Count(dhPrelude, "\n") + 1
	var want []int
	emit := func(text string, refusedAt int) {
		if refusedAt > 0 {
			want = append(want, line+refusedAt-1)
		}
		b.WriteString(text)
		line += strings.Count(text, "\n")
	}
	for i := range 8 {
		emit(dhFunc(r, i))
	}
	src := b.String()
	_, diags := checkFor(src)
	var got []int
	for _, d := range diags {
		if d.Code != "S108" {
			t.Fatalf("declared-handle seed %d: unexpected %v\nReplay: ERGO_SEED=%d go test -run '^TestOwnershipFlowModel$' ./internal/sketch/check\n%s", seed, d, seed, ofNumbered(src))
		}
		got = append(got, d.Line)
	}
	slices.Sort(got)
	got = slices.Compact(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		var full []string
		for _, d := range diags {
			full = append(full, d.String())
		}
		t.Fatalf("declared-handle seed %d (replay: ERGO_SEED=%d go test -run '^TestOwnershipFlowModel$' ./internal/sketch/check)\nsource:\n%s\nchecker lines: %v\nmodel lines:   %v\n%s", seed, seed, ofNumbered(src), got, want, strings.Join(full, "\n"))
	}
}

const dhPrelude = `import sync

type Inner { h sync.Ref[int] }

type Board { h sync.Ref[int], hs []sync.Ref[int], o Option[sync.Ref[int]], inner Inner, n int } implements Closer

func Close(b Board) {}

type CBoxref { v sync.Ref[int] } implements Closer
type CBoxopt { v Option[sync.Ref[int]] } implements Closer
type CBoxrefs { v []sync.Ref[int] } implements Closer
type CBoxint { v int } implements Closer

func Close(c CBoxref) {}
func Close(c CBoxopt) {}
func Close(c CBoxrefs) {}
func Close(c CBoxint) {}

type PBoxref { v sync.Ref[int] }
type PBoxopt { v Option[sync.Ref[int]] }
type PBoxrefs { v []sync.Ref[int] }
type PBoxint { v int }

func Useref(x sync.Ref[int]) int { x.Get() }
func Useopt(x Option[sync.Ref[int]]) int { 0 }
func Userefs(x []sync.Ref[int]) int { len(x) }
func Useint(x int) int { x }

func fresh() sync.Ref[int] { sync.NewRef[int](0) }

func main() {}
`

// dhTypes are the Ergo types of the parts' kinds.
var dhTypes = map[string]string{"ref": "sync.Ref[int]", "opt": "Option[sync.Ref[int]]", "refs": "[]sync.Ref[int]", "int": "int"}

// dhZero is a value of a result type that no board owns.
func dhZero(kind, prod string) string {
	v := map[string]string{"ref": "fresh()", "opt": "None", "refs": "[]sync.Ref[int]{}", "int": "0"}[kind]
	switch prod {
	case "call", "pass":
		return "0"
	case "closer":
		return "CBox" + kind + "{v: " + v + "}"
	case "plain":
		return "PBox" + kind + "{v: " + v + "}"
	case "slice", "taking":
		return "[]" + dhTypes[kind] + "{}"
	}
	return v
}

// dhFunc writes function i and the line, counted from its first, where
// the model refuses it (0: admitted).
func dhFunc(r *rand.Rand, i int) (string, int) {
	root := []string{"lent", "taken", "borrowed"}[r.IntN(3)]
	param, takes := "b Board", ""
	switch root {
	case "taken":
		takes = " takes b"
	case "borrowed":
		param = "b *Board"
	}
	name := fmt.Sprintf("F%d", i)
	switch k := r.IntN(10); {
	case k == 0 && root != "borrowed":
		// Take the board and update it: the board moves whole.
		text := fmt.Sprintf("func %s(%s) Board%s {\n\tBoard{..b, n: 1}\n}\n", name, param, takes)
		if root == "lent" {
			return text, 2
		}
		return text, 0
	case k == 1 && root == "borrowed":
		// Write the borrowed board in place: one owner.
		return fmt.Sprintf("func %s(b *Board) {\n\tb.hs = b.hs.Append(fresh())\n\tb.h = fresh()\n}\n", name), 0
	}
	// The part read, its kind, and how it is bound.
	type part struct{ path, kind string }
	parts := []part{{"b.h", "ref"}, {"b.hs[0]", "ref"}, {"b.inner.h", "ref"}, {"b.o", "opt"}, {"b.hs", "refs"}, {"b.n", "int"}, {"", "for"}, {"", "match"}}
	p := parts[r.IntN(len(parts))]
	kind := p.kind
	if kind == "for" || kind == "match" {
		kind = "ref"
	}
	prod := []string{"call", "pass", "return", "closer", "plain", "slice", "taking"}[r.IntN(7)]
	result := map[string]string{"call": "int", "pass": "int", "return": dhTypes[kind], "closer": "CBox" + kind, "plain": "PBox" + kind, "slice": "[]" + dhTypes[kind], "taking": "[]" + dhTypes[kind]}[prod]
	use := func(e string) string {
		switch prod {
		case "call":
			if kind == "ref" {
				return e + ".Get()"
			}
			return "Use" + kind + "(" + e + ")"
		case "pass":
			return "Use" + kind + "(" + e + ")"
		case "return":
			return e
		case "closer":
			return "CBox" + kind + "{v: " + e + "}"
		case "plain":
			return "PBox" + kind + "{v: " + e + "}"
		case "slice":
			return "[]" + dhTypes[kind] + "{" + e + "}"
		}
		return "[]" + dhTypes[kind] + "{}.Append(" + e + ")"
	}
	escapes := prod != "call" && prod != "pass" && kind != "int"
	alias := r.IntN(2) == 0
	var body []string
	at := 0 // the line of the use, counted from the function's first
	switch p.kind {
	case "for":
		e := "x"
		if alias {
			body = append(body, "for x0 in b.hs {", "\tlet x = x0")
		} else {
			body = append(body, "for x in b.hs {")
		}
		body = append(body, "\treturn "+use(e))
		at = len(body) + 1
		body = append(body, "}", dhZero(kind, prod))
	case "match":
		body = append(body, "match b.o {", "case Some[sync.Ref[int]] as s:")
		e := "s.Value"
		if alias {
			body = append(body, "\tlet x = s.Value")
			e = "x"
		}
		body = append(body, "\t"+use(e))
		at = len(body) + 1
		body = append(body, "case None:", "\t"+dhZero(kind, prod), "}")
	default:
		e := p.path
		if alias {
			body = append(body, "let x = "+p.path)
			e = "x"
		}
		body = append(body, use(e))
		at = len(body) + 1
	}
	var text strings.Builder
	fmt.Fprintf(&text, "func %s(%s) %s%s {\n", name, param, result, takes)
	for _, l := range body {
		text.WriteString("\t" + l + "\n")
	}
	text.WriteString("}\n")
	if !escapes {
		at = 0
	}
	return text.String(), at
}
