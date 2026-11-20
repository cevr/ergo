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
	..Writer,
	mut Written() string,
	mut raw(text string),
	mut count(n int),

	law RawJoins(a string, b string) { raw(a); raw(b) } == { raw(a + b) },
}

type tape { out string } implements Tape

func raw(t mut tape, s string) { t.out = t.out + s }
func count(t mut tape, n int) {
	if n < 10 {
		t.raw(digit(n))
	} else {
		t.raw("+")
	}
}
func digit(n int) string { "0123456789"[n:n + 1] }
func Written(t mut tape) string { t.out }
func Null(t mut tape) { t.raw("n") }
func Number(t mut tape, text string) { t.raw(text) }
func Bool(t mut tape, b bool) { t.raw("b") }
func Int(t mut tape, n int64) { t.raw("i") }
func Uint(t mut tape, n uint64) { t.raw("u") }
func Float(t mut tape, f float64) { t.raw("f") }
func String(t mut tape, s string) { t.raw(s) }
func StringBytes(t mut tape, b []byte) { t.raw("s") }
func BeginList(t mut tape, n int) { t.raw("[") }
func EndList(t mut tape) { t.raw("]") }
func BeginObject(t mut tape, n int) { t.raw("{") }
func Tag(t mut tape, name string) { t.raw(name) }
func Key(t mut tape, k string) {
	t.count(len(k))
	t.raw(k)
}
func EndObject(t mut tape) { t.raw("}") }

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
