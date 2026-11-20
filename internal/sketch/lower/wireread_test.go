package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// cellReaderSource is a package with its own direct reader, wrap (a
// json.Reader behind each of the prelude Reader's operations), and a type
// with a read of its own on it (types.md, Codecs 11): pair reads the
// list [a, b] from a wrap, and its ordinary form, the object {"a", "b"},
// from every other reader. direct decodes on a wrap (the direct path),
// viaReader on the prelude's Reader holding a wrap (the interface path,
// through wrap's operations adapter), plain on a json.Reader (the
// ordinary form). The law pairReadsOneValue is the one rule 11 requires.
const cellReaderSource = `import json

type wrap { j json.Reader } implements Reader

func TakeNull(w wrap) bool { w.j.TakeNull() }
func Peek(w wrap) WireKind { w.j.Peek() }
func Number(w wrap) string fails WireWrongKind { try w.j.Number() }
func Path(w wrap) string { w.j.Path() }
func Bool(w wrap) bool fails WireWrongKind { try w.j.Bool() }
func Int(w wrap) int64 fails WireWrongKind { try w.j.Int() }
func Uint(w wrap) uint64 fails WireWrongKind { try w.j.Uint() }
func Float(w wrap) float64 fails WireWrongKind { try w.j.Float() }
func String(w wrap) string fails WireWrongKind { try w.j.String() }
func StringBytes(w wrap, into []byte) []byte fails WireWrongKind { try w.j.StringBytes(into) }
func BeginList(w wrap) fails WireWrongKind { try w.j.BeginList() }
func BeginObject(w wrap) fails WireWrongKind { try w.j.BeginObject() }
func Tag(w wrap) string fails WireMissingField { try w.j.Tag() }
func More(w wrap) bool { w.j.More() }
func Key(w wrap) string { w.j.Key() }
func Skip(w wrap) { w.j.Skip() }
func End(w wrap) { w.j.End() }

type pair { A int, B int }

// WireRead reads a pair from a wrap as the list [a, b].
func WireRead(p mut pair, w wrap) fails WireWrongKind {
	try w.BeginList()
	p.A = int(try w.Int())
	p.B = int(try w.Int())
	w.End()
}

// pairReadsOneValue: a pair between two sentinels reads back with both.
law pairReadsOneValue(x pair) {
	let j = json.Open("[7,[\(x.A),\(x.B)],7]", 0, 0) catch {
	case _:
		return false
	}
	let (a, p, b) = decode[(int, pair, int)](wrap{j: j}) catch {
	case _:
		return false
	}
	a == 7 && p == x && b == 7
}

func said(a int, p pair, b int) string { "\(a) \(p.A) \(p.B) \(b)" }

func direct(w wrap) string {
	let (a, p, b) = decode[(int, pair, int)](w) catch {
	case WireWrongKind as e:
		return "wrong \(e.Path) \(e.Want)"
	case WireMissingField as e:
		return "missing \(e.Path) \(e.Field)"
	}
	said(a, p, b)
}

func viaReader(r Reader) string {
	let (a, p, b) = decode[(int, pair, int)](r) catch {
	case WireWrongKind as e:
		return "wrong \(e.Path) \(e.Want)"
	case WireMissingField as e:
		return "missing \(e.Path) \(e.Field)"
	}
	said(a, p, b)
}

func plain(j json.Reader) string {
	let (a, p, b) = decode[(int, pair, int)](j) catch {
	case WireWrongKind as e:
		return "wrong \(e.Path) \(e.Want)"
	case WireMissingField as e:
		return "missing \(e.Path) \(e.Field)"
	}
	said(a, p, b)
}

// reads is the three decodes of text, each on its own reader.
func reads(text string) string {
	let j1 = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	let j2 = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	let j3 = json.Open(text, 0, 0) catch {
	case _:
		return "malformed"
	}
	"\(direct(wrap{j: j1})) | \(viaReader(wrap{j: j2})) | \(plain(j3))"
}
`

// cellReaderTexts are seeded texts around the pair: its own form, its
// ordinary form, and the near misses of each (a short or long list, a
// string for a number, a missing sentinel), so each path reads, fails and
// fails at a path.
func cellReaderTexts(rng *rand.Rand) []string {
	n := func() string { return strconv.Itoa(rng.Intn(19) - 9) }
	middles := []func() string{
		func() string { return "[" + n() + "," + n() + "]" },
		func() string { return "[" + n() + "]" },
		func() string { return "[" + n() + "," + n() + "," + n() + "]" },
		func() string { return "[\"x\"," + n() + "]" },
		func() string { return "{\"a\":" + n() + ",\"b\":" + n() + "}" },
		func() string { return "{\"a\":" + n() + "}" },
		func() string { return "[]" },
		func() string { return n() },
	}
	var out []string
	for range 24 {
		m := middles[rng.Intn(len(middles))]()
		switch rng.Intn(4) {
		case 0:
			out = append(out, "["+n()+","+m+"]")
		default:
			out = append(out, "["+n()+","+m+","+n()+"]")
		}
	}
	return out
}

// TestCellReaderAgrees: a type's own read (types.md, Codecs 11) reads the
// same value, and fails the same failure at the same path, on the direct
// path (a decode on the reader R), on the Reader interface holding an R
// (the adapter's check in wireread.go), and in the evaluator (codecwalk
// ownRead); a decode on another reader reads the ordinary form. It also
// runs the law rule 11 requires (pairReadsOneValue) in the evaluator.
// Mutants: wr-direct-ordinary (the direct path reads the ordinary form),
// wr-iface-ordinary (the interface path skips the adapter's check),
// wr-eval-ordinary (eval's ownRead never applies). ERGO_CELL_READER_SEED
// replays a seed.
//
// It builds Go because the direct and interface paths under test are the
// lowered Go's codec functions; the evaluator runs the Ergo, which is the
// third side compared.
func TestCellReaderAgrees(t *testing.T) {
	needsBuild(t)
	seed := int64(1)
	if s := os.Getenv("ERGO_CELL_READER_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	replay := fmt.Sprintf("replay: ERGO_CELL_READER_SEED=%d go test -run TestCellReaderAgrees ./internal/sketch/lower", seed)
	texts := cellReaderTexts(rand.New(rand.NewSource(seed)))
	var main strings.Builder
	main.WriteString("\nfunc main() needs Console {\n")
	for i, text := range texts {
		fmt.Fprintf(&main, "\tConsole.Println(\"%d \\(reads(%s))\")\n", i, strconv.Quote(text))
	}
	main.WriteString("}\n")
	src := cellReaderSource + main.String()
	o := lowerSource("cellreader", src, true)
	if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
		t.Fatalf("seed %d: lowering: err %v, panic %v, diags %v, refused %v\n%s", seed, o.err, o.panicked, o.diags, o.refused, replay)
	}
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatalf("seed %d: no build\n%s", seed, replay)
	}
	out, stderr, code := runExe(t, exes[o.name])
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s\n%s", seed, code, firstLines(stderr, 8), replay)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(texts) {
		t.Fatalf("seed %d: %d lines for %d texts:\n%s\n%s", seed, len(lines), len(texts), out, replay)
	}
	// The direct and interface paths agree on every text, and the
	// ordinary form differs on the own form's texts (the read is taken).
	own := 0
	for i, line := range lines {
		parts := strings.Split(strings.TrimPrefix(line, strconv.Itoa(i)+" "), " | ")
		if len(parts) != 3 {
			if parts[0] == "malformed" {
				continue
			}
			t.Fatalf("seed %d: line %q\n%s", seed, line, replay)
		}
		if parts[0] != parts[1] {
			t.Errorf("seed %d: text %s: the direct path reads %q, the Reader interface %q\nsource:\n%s\n%s", seed, texts[i], parts[0], parts[1], src, replay)
		}
		if parts[0] != parts[2] {
			own++
		}
	}
	if own == 0 {
		t.Errorf("seed %d: no text reads differently by the own read and the ordinary form; the seed reaches neither\n%s", seed, replay)
	}
	// The evaluator reads as the lowered Go: a test of the same package
	// holds each line the program printed, and the law runs.
	var tb strings.Builder
	tb.WriteString("\ntest EvalReadsAsGo() {\n")
	for i, text := range texts {
		want := strings.TrimPrefix(lines[i], strconv.Itoa(i)+" ")
		fmt.Fprintf(&tb, "\tassert reads(%s) == %s\n", strconv.Quote(text), strconv.Quote(want))
	}
	tb.WriteString("}\n")
	file, err := syntax.Parse(cellReaderSource + tb.String())
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("seed %d: %v\n%s", seed, diags, replay)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rep.Results {
		seen[r.Name] = true
		if r.Status != eval.Passed {
			t.Errorf("seed %d: eval %s: %s %v\n%s", seed, r.Name, r.Status, r, replay)
		}
	}
	for _, name := range []string{"EvalReadsAsGo", "law pairReadsOneValue"} {
		if !seen[name] {
			t.Errorf("seed %d: eval ran no %s (ran %v)\n%s", seed, name, seen, replay)
		}
	}
}
