package lower

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The cell writer property (wiretext.go). std json's Text enters every
// value position after setComma(false) and ends every run in setComma, so
// json reaches only part of the cell writer lowering. Each generated
// program declares its own cell writer: Tape, sealed in the program,
// whose one provider tape keeps the last key (mark) and the text, and
// whose laws are true by construction: RawJoins, MarkLast (mark is
// idempotent) and EchoMark (a mark then echo writes the mark). The
// provider's event bodies are drawn: an event ends in an idempotent mark
// or in a constant raw that is not idempotent, and drops its argument or
// writes it. Record types hold the drawn fields: scalars, Option, json's
// Optional and Nullable, lists and the records before them, so a codec
// function is entered in several states (keys) and runs end before arms.
// The program is lowered three ways: the default lowering with every law
// admitted (the engine under test; the laws are true), the reference
// (rewriteLowering "reference") and the Writer path (wireLowering
// "writer"). All three must print the same.

// tapeEvents are the drawn event bodies: each Writer event's name, its
// parameter and the bodies it may take ({} is the parameter's name, $ a
// tail drawn from tapeTails).
var tapeEvents = []struct {
	name, param string
	bodies      []string
}{
	{"Null", "", []string{`t.raw("n")$`}},
	{"Number", "s string", []string{`t.raw(s)$`}},
	{"Bool", "b bool", []string{`t.raw("b")$`, `t.raw("\(b)")$`}},
	{"Int", "n int64", []string{`t.raw("\(n)")$`, `t.raw("i")$`}},
	{"Uint", "n uint64", []string{`t.raw("\(n)")$`}},
	{"Float", "f float64", []string{`t.raw("f")$`}},
	{"String", "s string", []string{`if empty(s) { t.raw("e") } else { t.raw(s) }`, `t.raw(s)$`}},
	{"BeginList", "n int", []string{`t.raw("[")$`, `t.echo(); t.raw("[")$`}},
	{"EndList", "", []string{`t.raw("]")$`}},
	// BeginObject ends in a constant raw, never idempotent, so a record
	// whose first field is optional ends a run before an arm.
	{"BeginObject", "n int", []string{`t.echo(); t.raw("{")`}},
	{"Tag", "name string", []string{`t.raw(name)$`, `t.raw("#")$`}},
	// Key ends in mark(k): the state a field's codec call starts in is
	// its key's, so one record type entered at two keys asks for two
	// entry states.
	{"Key", "k string", []string{`t.raw(k); t.mark(k)`}},
	{"EndObject", "", []string{`t.raw("}")$`}},
}

var tapeTails = []string{"", `; t.mark("")`}

// tapeSource is the Tape cell writer with drawn event bodies.
func tapeSource(rng *rand.Rand) string {
	var b strings.Builder
	b.WriteString(`import json

type Tape {
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
	mark(k string),
	echo(),

	law RawJoins(a string, b string) { raw(a); raw(b) } == { raw(a + b) },
	law MarkLast(a string, b string) { mark(a); mark(b) } == { mark(b) },
	law EchoMark(a string) { mark(a); echo() } == { mark(a); raw(a + "") },
}

type tape { last string, out string }

func NewTape() Tape { tape{last: "", out: ""} }
func raw(t *tape, s string) { t.out = t.out + s }
func mark(t *tape, k string) { t.last = k }
func echo(t *tape) { t.out = t.out + t.last }
func Written(t *tape) string { t.out }
func empty(s string) bool { s == "" }
`)
	for _, e := range tapeEvents {
		body := e.bodies[rng.Intn(len(e.bodies))]
		body = strings.ReplaceAll(body, "$", tapeTails[rng.Intn(len(tapeTails))])
		params := "t *tape"
		if e.param != "" {
			params += ", " + e.param
		}
		fmt.Fprintf(&b, "func %s(%s) { %s }\n", e.name, params, body)
	}
	return b.String()
}

// tapeField is a drawn field type: its Ergo type and a drawer of values.
type tapeField struct {
	typ   string
	value func(rng *rand.Rand, depth int) string
}

// tapeRecords draws record types r0, r1, ...; each field's type is a
// scalar, a wrapper, a list of leaf0 or an earlier record. Three fixed
// records come first: pair0 holds one record at two keys, lead0 starts
// with an optional field (an arm right after BeginObject's run), list0
// holds a list of records.
func tapeRecords(rng *rand.Rand) (src string, kinds []tapeField) {
	str := func(rng *rand.Rand) string { return strconv.Quote([]string{"", "s", "q1", "key"}[rng.Intn(4)]) }
	scalars := []tapeField{
		{"int", func(rng *rand.Rand, _ int) string { return strconv.Itoa(rng.Intn(9)) }},
		{"bool", func(rng *rand.Rand, _ int) string { return []string{"true", "false"}[rng.Intn(2)] }},
		{"string", func(rng *rand.Rand, _ int) string { return str(rng) }},
		{"Option[int]", func(rng *rand.Rand, _ int) string {
			if rng.Intn(2) == 0 {
				return "None"
			}
			return "Some{Value: " + strconv.Itoa(rng.Intn(9)) + "}"
		}},
		{"json.Optional[int]", func(rng *rand.Rand, _ int) string {
			if rng.Intn(2) == 0 {
				return "json.Absent"
			}
			return "json.Present{Value: " + strconv.Itoa(rng.Intn(9)) + "}"
		}},
		{"json.Nullable[bool]", func(rng *rand.Rand, _ int) string {
			if rng.Intn(2) == 0 {
				return "json.Null"
			}
			return "json.NonNull{Value: true}"
		}},
		{"[]int", func(rng *rand.Rand, _ int) string {
			var xs []string
			for range rng.Intn(3) {
				xs = append(xs, strconv.Itoa(rng.Intn(9)))
			}
			return "[]int{" + strings.Join(xs, ", ") + "}"
		}},
	}
	var b strings.Builder
	record := func(name string, fields []tapeField) tapeField {
		b.WriteString("type " + name + " {")
		for i, f := range fields {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, " F%d %s", i, f.typ)
		}
		b.WriteString(" }\n")
		return tapeField{name, func(rng *rand.Rand, depth int) string {
			var parts []string
			for i, f := range fields {
				parts = append(parts, fmt.Sprintf("F%d: %s", i, f.value(rng, depth+1)))
			}
			return name + "{" + strings.Join(parts, ", ") + "}"
		}}
	}
	leaf := record("leaf0", []tapeField{scalars[0]})
	// A list of records: each element's codec call is in a loop, whose
	// state is unknown at its second element.
	leaves := tapeField{"[]leaf0", func(rng *rand.Rand, depth int) string {
		var xs []string
		for range rng.Intn(3) {
			xs = append(xs, leaf.value(rng, depth+1))
		}
		return "[]leaf0{" + strings.Join(xs, ", ") + "}"
	}}
	kinds = append(kinds, record("pair0", []tapeField{leaf, leaf}), record("lead0", []tapeField{scalars[4], scalars[1]}), record("list0", []tapeField{leaves}))
	known := append([]tapeField{leaf, leaves}, kinds...)
	for i := range 4 {
		var fields []tapeField
		for range 1 + rng.Intn(4) {
			if rng.Intn(3) == 0 {
				fields = append(fields, known[rng.Intn(len(known))])
			} else {
				fields = append(fields, scalars[rng.Intn(len(scalars))])
			}
		}
		r := record("r"+strconv.Itoa(i), fields)
		known = append(known, r)
		kinds = append(kinds, r)
	}
	return b.String(), kinds
}

// tapeProgram is the program of seed: the Tape, the records and a main
// that prints the text of drawn values, one Tape each.
func tapeProgram(seed int64) string {
	rng := rand.New(rand.NewSource(seed))
	src := tapeSource(rng)
	records, kinds := tapeRecords(rng)
	var b strings.Builder
	b.WriteString(src + "\n" + records + "\nfunc main() needs Console {\n")
	for i := range 12 {
		k := kinds[i%len(kinds)]
		fmt.Fprintf(&b, "\tlet t%d = NewTape()\n\tencode(%s, t%d)\n\tConsole.Println(\"%d \\(t%d.Written())\")\n", i, k.value(rng, 0), i, i, i)
	}
	b.WriteString("}\n")
	return b.String()
}

// TestStdEvidenceAdmitsOnlyProvedCurrent: for every law of std json's
// provider, the std ledger's evidence admits the law exactly when its
// entry is proved and its digest current; the entry made tested, or its
// digest changed, is refused (owner q115: only proved laws change code).
// The texts cannot show it: a tested law that is true writes the same
// text. Mutants wt-admit-tested, wt-admit-stale.
func TestStdEvidenceAdmitsOnlyProvedCurrent(t *testing.T) {
	const src = "import json\n\ntype X { A int }\n\nfunc main() needs Console {\n\tlet t = json.NewText()\n\tencode(X{A: 1}, t)\n\tConsole.Println(t.Written())\n}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	var jsonPkg *check.Package
	for _, ty := range pkg.Info.Types {
		if n, ok := ty.(*check.Named); ok && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "json" && n.Obj.Name == "Text" {
			jsonPkg = n.Obj.Pkg
		}
	}
	if jsonPkg == nil {
		t.Fatal("no json.Text in the checked program")
	}
	e := newStdEvidence(jsonPkg)
	admitted := 0
	for prov, rules := range newRewriter(jsonPkg, nil).rules {
		for _, r := range rules {
			key := ledgerKey(LedgerNames(nil).law(r.law), LedgerNames(nil).provider(prov), "")
			entry, found := e.entries[key]
			got, why := e.Admit(r.law, prov, RewriteInstance{})
			if want := found && entry.Verdict == "proved"; got != want {
				t.Fatalf("%s (%+v): admitted %v, want %v (%s)", key, entry, got, want, why)
			}
			if !got {
				continue
			}
			admitted++
			for _, bad := range []LedgerEntry{{Verdict: "tested", Digest: entry.Digest}, {Verdict: "proved", Digest: "h1:stale"}} {
				e.entries[key] = bad
				if ok, _ := e.Admit(r.law, prov, RewriteInstance{}); ok {
					t.Fatalf("%s: admitted with the entry %+v", key, bad)
				}
			}
			e.entries[key] = entry
		}
	}
	if admitted == 0 {
		t.Fatal("the std ledger admits no law of json's provider")
	}
}

// TestCellWriterAgrees: a program's own cell writer encodes what the
// reference and the Writer path encode (wiretext.go). Mutants:
// lr3-w5 (a run's last call taken as idempotent), lr3-w7 (a codec
// function's entry variant used for another state) and wt-unused-arg (a
// dropped non-constant event argument left its Go variable unused).
// ERGO_CELL_WRITER_SEED replays a seed.
//
// It builds Go because the property compares the printed text of three
// lowerings of one program; the reference interpreter runs Ergo, not the
// lowered Go whose rewritten runs are under test.
func TestCellWriterAgrees(t *testing.T) {
	needsBuild(t)
	seed := int64(1)
	if s := os.Getenv("ERGO_CELL_WRITER_SEED"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = v
	}
	src := tapeProgram(seed)
	replay := fmt.Sprintf("replay: ERGO_CELL_WRITER_SEED=%d go test -run TestCellWriterAgrees ./internal/sketch/lower", seed)
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("seed %d: %v\n%s\n%s", seed, err, src, replay)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("seed %d: %v\n%s\n%s", seed, diags, src, replay)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}, Rewrite: admitAll{}}
	savedRewrite, savedWire := rewriteLowering, wireLowering
	defer func() { rewriteLowering, wireLowering = savedRewrite, savedWire }()
	var progs []*lowered
	for _, mode := range []struct{ name, rewrite, wire string }{{"fast", savedRewrite, savedWire}, {"reference", "reference", savedWire}, {"writer", savedRewrite, "writer"}} {
		rewriteLowering, wireLowering = mode.rewrite, mode.wire
		files, err := Lower(u)
		if err != nil {
			t.Fatalf("seed %d: %s lowering: %v\n%s\n%s", seed, mode.name, err, src, replay)
		}
		progs = append(progs, &lowered{name: mode.name, src: src, file: file, pkg: pkg, files: files})
	}
	rewriteLowering, wireLowering = savedRewrite, savedWire
	exes := buildAll(t, progs, true)
	if exes == nil {
		t.Fatalf("seed %d: no build\n%s", seed, replay)
	}
	want, _, code := runExe(t, exes["reference"])
	if code != 0 {
		t.Fatalf("seed %d: the reference exits %d\n%s\n%s", seed, code, src, replay)
	}
	for _, name := range []string{"fast", "writer"} {
		got, stderr, code := runExe(t, exes[name])
		if got != want || code != 0 {
			t.Fatalf("seed %d: %s prints\n%s(exit %d, %s)\nthe reference\n%s\nsource:\n%s\n%s", seed, name, got, code, firstLines(stderr, 8), want, src, replay)
		}
	}
}
