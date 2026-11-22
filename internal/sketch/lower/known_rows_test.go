package lower

import (
	"encoding/json"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// knownRowsSrc holds the Option combinators of ADT-NOPRIV's probe, each
// row-polymorphic in its callback (fails E needs R), called at a known pure
// literal (KNOWN-VARIANT K1).
const knownRowsSrc = `
func optMap[T, U, E, R](o Option[T], f func(v T) U fails E needs R) Option[U] fails E needs R {
	match o {
	case Some[T] as s:
		Some{Value: try f(s.Value)}
	case None:
		None
	}
}

func optThen[T, U, E, R](o Option[T], f func(v T) Option[U] fails E needs R) Option[U] fails E needs R {
	match o {
	case Some[T] as s:
		try f(s.Value)
	case None:
		None
	}
}

func optFold[T, U, E, R](o Option[T], d U, f func(v T) U fails E needs R) U fails E needs R {
	match o {
	case Some[T] as s:
		try f(s.Value)
	case None:
		d
	}
}

func optOrElse[T, E, R](o Option[T], f func() T fails E needs R) T fails E needs R {
	match o {
	case Some[T] as s:
		s.Value
	case None:
		try f()
	}
}

func optFilter[T, E, R](o Option[T], f func(v T) bool fails E needs R) Option[T] fails E needs R {
	match o {
	case Some[T] as s:
		if try f(s.Value) {
			o
		} else {
			None
		}
	case None:
		None
	}
}

func read(m Map[int, int], k int) int {
	let a = optMap(m.Get(k), func(v int) int { v + 1 })
	let b = optThen(a, func(v int) Option[int] { Some{Value: v * 2} })
	let c = optFilter(b, func(v int) bool { v > 3 })
	let d = optFold(c, 0, func(v int) int { v - 1 })
	d + optOrElse(m.Get(k + 1), func() int { 7 })
}

func main() needs Console {
	let m = Map[int, int]{1: 2}
	Console.Println("\(read(m, 1) + read(m, 3))")
}
`

var knownRowsCombinators = []string{"optMap", "optThen", "optFold", "optOrElse", "optFilter"}

// TestKnownVariantRowInstance: a combinator whose callback carries row
// parameters of its own (fails E needs R) takes its known variant at a
// call that passes a known pure literal: the variant has no entry poll and
// calls the callback directly, on the Go text and on the IR (KNOWN-VARIANT
// K1). It kills kv-rows-ignored: with the declaration's rows refused, as
// before K1, no combinator has a variant.
func TestKnownVariantRowInstance(t *testing.T) {
	var stats knownStats
	saved := statsHook
	statsHook = func(s knownStats) { stats = s }
	o := lowerSource("knownrows", knownRowsSrc, true)
	statsHook = saved
	if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v %v", o.err, o.diags, o.refused, o.panicked)
	}
	text := goText(o)
	if stats.Sites == 0 {
		t.Error("text: no call goes to a known variant")
	}
	read := funcText(text, "func read(")
	for _, name := range knownRowsCombinators {
		// The call goes to the variant, or is inlined with its literal
		// substituted (inline.go), which leaves no call at all.
		if strings.Contains(read, name+"(") || strings.Contains(read, name+"Mono") {
			t.Errorf("text: read calls %s itself, not its known variant", name)
		}
		body := funcText(text, "func "+name+"Known")
		if body == "" {
			if !strings.Contains(read, name+"Known") {
				continue // inlined
			}
			t.Errorf("text: %s has no known variant", name)
			continue
		}
		if strings.Contains(body, ".Check()") {
			t.Errorf("text: %sKnown polls at entry:\n%s", name, body)
		}
		if strings.Contains(body, "f(env") {
			t.Errorf("text: %sKnown calls its callback through the uniform shape:\n%s", name, body)
		}
	}
	if t.Failed() {
		t.Logf("read:\n%s", funcText(text, "func read("))
	}
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: knownRowsSrc}}}
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	var ir struct {
		Funcs []struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		} `json:"funcs"`
	}
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	bodies := map[string]string{}
	for _, f := range ir.Funcs {
		bodies[f.Name] = string(f.Body)
	}
	irRead, ok := bodies["read"]
	if !ok {
		t.Fatal("IR: read is not on the IR")
	}
	for _, name := range knownRowsCombinators {
		if strings.Contains(irRead, `"`+name+`"`) || strings.Contains(irRead, `"`+name+`Mono`) {
			t.Errorf("IR: read calls %s itself, not its known variant", name)
		}
		if body, ok := bodies[name+"Known"]; ok && strings.Contains(body, `"Check"`) {
			t.Errorf("IR: %sKnown polls at entry", name)
		}
		// try of the plain callback is the call alone: no (value, error)
		// pair, which its one result cannot bind.
		if body, ok := bodies[name+"Known"]; ok && strings.Contains(body, `"def2"`) {
			t.Errorf("IR: %sKnown binds its plain callback's call as a (value, error) pair", name)
		}
	}
}

// knownSharedRowSrc shares the row E between a known pure literal and a
// literal that fails: E is not empty at the call, so the call keeps the
// uniform shape, and the failure reaches the caller.
const knownSharedRowSrc = `
type Boom {}

func both[E](o Option[int], f func(v int) int fails E, g func() int fails E) int fails E {
	match o {
	case Some[int] as s:
		try f(s.Value)
	case None:
		try g()
	}
}

func run(o Option[int]) int fails Boom {
	try both(o, func(v int) int { v + 1 }, func() int fails Boom { return Boom })
}

func main() needs Console {
	let n = run(None) catch {
	case Boom:
		-1
	}
	Console.Println("\(n)")
}
`

// TestKnownVariantSharedRow: a call that passes one known pure literal and
// one that fails, through a shared row, takes no known variant (every
// callback must be known), on the text and on the IR. It kills
// kv-impure-callback-taken: a variant there would call the failing
// literal plain and lose its failure.
func TestKnownVariantSharedRow(t *testing.T) {
	o := lowerSource("knownshared", knownSharedRowSrc, true)
	if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v %v", o.err, o.diags, o.refused, o.panicked)
	}
	if text := goText(o); strings.Contains(text, "bothKnown") {
		t.Errorf("text: the call with a failing callback goes to a known variant:\n%s", funcText(text, "func run("))
	}
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: knownSharedRowSrc}}}
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == IRFileName && strings.Contains(string(f.Data), "bothKnown") {
			t.Error("IR: the call with a failing callback goes to a known variant")
		}
	}
}

// TestKnownRowsRun runs both programs: the shared row's failure reaches
// the caller (-1), and the combinators' variants compute what the plain
// path computes (read(m, 1) = 2+1=3, *2=6, >3 kept, -1 = 5, plus the
// missing key 2's 7 = 12; read(m, 3) = None… = 0 + 7 = 7; 19).
//
// It builds Go because the question is what the built program prints: a
// failure lost by a plain call is seen only in the run.
func TestKnownRowsRun(t *testing.T) {
	needsBuild(t)
	wantRun(t, knownSharedRowSrc, "-1\n")
	wantRun(t, knownRowsSrc, "19\n")
	wantRun(t, knownLoopSrc, "512\n")
}

// knownLoopSrc calls optFilter, the combinator Go's inliner declines, with
// a known pure literal in a loop: absencebench's filter kind
// (KNOWN-FILTER-INLINE). Keys 0..2047 against the 1024 even keys, each
// valued k/2: a read keeps its value when k/2 is even (k ≡ 0 mod 4), so
// 512 of the 2048 reads are Some.
const knownLoopSrc = `
func optFilter[T, E, R](o Option[T], p func(v T) bool fails E needs R) Option[T] fails E needs R {
	match o {
	case Some[T] as s:
		if try p(s.Value) {
			return s
		}
		None
	case None:
		None
	}
}

func filters(m Map[int, int], n int) int {
	let mut t = 0
	for i in 0..n {
		match optFilter(m.Get(i & 2047), func(v int) bool { v & 1 == 0 }) {
		case Some[int]:
			t = t + 1
		case None:
			t = t + 0
		}
	}
	t
}

func main() needs Console {
	let mut m = Map[int, int]{}
	for i in 0..1024 {
		m = m.Put(i * 2, i)
	}
	Console.Println("\(filters(m, 2048) + filters(m, 0))")
}
`

// TestKnownVariantInlined: on the IR, a call of a known variant in a loop
// is inlined with its literal bound at the copy's head, so no call is
// left per element (KNOWN-FILTER-INLINE). It kills kfi-callback-callee-
// refused and kfi-known-footprint-refused: with either admission
// condition undone, the call to optFilterKnown stays.
func TestKnownVariantInlined(t *testing.T) {
	o := lowerSource("knownloop", knownLoopSrc, true)
	if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v %v", o.err, o.diags, o.refused, o.panicked)
	}
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: knownLoopSrc}}}
	files, report, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if report.KnownInlined == 0 {
		t.Error("IR: no call of a known variant is inlined")
	}
	var ir struct {
		Funcs []struct {
			Name string          `json:"name"`
			Body json.RawMessage `json:"body"`
		} `json:"funcs"`
	}
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, f := range ir.Funcs {
		if f.Name == "filters" && strings.Contains(string(f.Body), `"optFilterKnown"`) {
			t.Errorf("IR: filters still calls optFilterKnown per element:\n%s", f.Body)
		}
	}
	// The refusal: a variant past the inline budget keeps its call. It
	// kills kfi-budget-ignored.
	big := strings.Replace(knownLoopSrc, "if try p(s.Value) {", "if try p(s.Value)"+strings.Repeat(" && try p(s.Value)", 40)+" {", 1)
	ob := lowerSource("knownbig", big, true)
	if ob.err != nil || len(ob.diags) > 0 || ob.refused != nil || ob.panicked != nil {
		t.Fatalf("lowering the big variant failed: %v %v %v %v", ob.err, ob.diags, ob.refused, ob.panicked)
	}
	ub := Unit{Package: ob.pkg, Files: []*syntax.File{ob.file}, Sources: []Source{{Path: testSourcePath, Text: big}}}
	if _, rep, err := LowerIR(ub); err != nil {
		t.Fatal(err)
	} else if rep.KnownInlined != 0 {
		t.Errorf("IR: a known variant past the inline budget is inlined (%d sites)", rep.KnownInlined)
	}
}
