package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// unfoldFoldSource is a cell writer whose Key passes a computed argument,
// count(len(k)), as a length-prefixed codec writes a key's head (CBOR,
// examples/cborpack): count's body is an if on its argument, each arm a
// constant append, the short arm a slice of a constant.
const unfoldFoldSource = `type Tape {
	Null(),
	Number(text string),
	Bool(b bool),
	Int(n int64),
	Uint(n uint64),
	Float(f float64),
	String(s string),
	BeginList(n int),
	EndList(),
	BeginObject(n int),
	Tag(name string),
	Key(k string),
	EndObject(),
	Written() string,
	raw(text string),
	count(n int),

	law RawJoins(a string, b string) { raw(a); raw(b) } == { raw(a + b) },
}

type tape { out string }

func raw(t *tape, s string) { t.out = t.out + s }
func count(t *tape, n int) {
	if n < 10 {
		t.raw(digit(n))
	} else {
		t.raw("+")
	}
}
func digit(n int) string { "0123456789"[n:n + 1] }
func Written(t *tape) string { t.out }
func Null(t *tape) { t.raw("n") }
func Number(t *tape, text string) { t.raw(text) }
func Bool(t *tape, b bool) { t.raw("b") }
func Int(t *tape, n int64) { t.raw("i") }
func Uint(t *tape, n uint64) { t.raw("u") }
func Float(t *tape, f float64) { t.raw("f") }
func String(t *tape, s string) { t.raw(s) }
func BeginList(t *tape, n int) { t.raw("[") }
func EndList(t *tape) { t.raw("]") }
func BeginObject(t *tape, n int) { t.raw("{") }
func Tag(t *tape, name string) { t.raw(name) }
func Key(t *tape, k string) {
	t.count(len(k))
	t.raw(k)
}
func EndObject(t *tape) { t.raw("}") }

type Rec { Name string, Description string }

func show(v Rec) string {
	let t Tape = tape{out: ""}
	encode(v, t)
	t.Written()
}

func main() needs Console { Console.Println(show(Rec{Name: "a", Description: "b"})) }
`

// TestUnfoldFoldsAComputedArgument: a window call whose body passes an
// expression of its parameters unfolds when the check-time evaluator folds
// that expression from the call's constants (types.md, Laws 14, Unfold by
// definition), so a record's object start and each key's computed head
// join into one constant append, as std json's keys do. It is the row of
// the rule that lets a module's length-prefixed codec reach that form
// (NOPRIV-JSON); the agreement of the folded text with the reference is
// TestCellWriterAgrees's, over drawn bodies.
func TestUnfoldFoldsAComputedArgument(t *testing.T) {
	file, err := syntax.Parse(unfoldFoldSource)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	files, err := Lower(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: unfoldFoldSource}}, Rewrite: admitAll{}})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		b.Write(f.Data)
	}
	enc := cellEvidenceEncode(b.String())
	for _, want := range []string{`raw(string("{4name"))`, `raw(string("+description"))`} {
		if !strings.Contains(enc, want) {
			t.Errorf("the encode of Rec has no %s: a key's computed head is not folded\n%s", want, enc)
		}
	}
}
