package lower

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestFallbackOffRefuses checks the switch F8's precondition reads
// (FALLBACK-OFF, ir_fallback.go): with Unit.NoFallback, a program whose
// IR lowering keeps bodies on the text path is refused with each such
// body and the reason the builder stopped at, exactly the report's
// fallbacks; a program with none lowers to the same files with the switch
// as without. It catches the ledger's mutant fallback-off-ignored (the
// switch never records a body). First's type, a named slice, is a known
// stop ("type defined over a non-basic type"): when the IR covers it, the
// test says so and needs another known stop. The program without stops
// drops Twice's value, so its variant with no result is the IR's
// (DROPPED-IR; I-FLIP #35: it fell back as "variant with no result").
func TestFallbackOffRefuses(t *testing.T) {
	const stops = `type Ids []int

func First(xs Ids) int {
	xs[0]
}

type P { A int, B int }

func Twice(p P) P {
	P{A: p.A * 2, B: p.B}
}

func main() {
	let _ = First(Ids([]int{1}))
	let _ = Twice(P{A: 1, B: 2})
}
`
	_, rep, err := LowerIR(factsUnit(t, stops))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fallback["type defined over a non-basic type"] == 0 {
		t.Fatalf("the IR lowers First now (fallbacks %v): give the test another known stop", rep.Fallback)
	}
	u := factsUnit(t, stops)
	u.NoFallback = true
	_, _, err = LowerIR(u)
	var refused *FallbackRefused
	if !errors.As(err, &refused) {
		t.Fatalf("with the switch the lowering is not refused: %v", err)
	}
	got := map[string]int{}
	for _, f := range refused.Funcs {
		if len(f.Reasons) != 1 {
			t.Errorf("%s has reasons %v, want its one stop", f.Name, f.Reasons)
		}
		for _, r := range f.Reasons {
			got[r]++
		}
	}
	if !maps.Equal(got, rep.Fallback) {
		t.Errorf("refused %v, the report's fallbacks are %v", got, rep.Fallback)
	}
	if !slices.ContainsFunc(refused.Funcs, func(f IRCensusFunc) bool { return f.Name == "First" }) ||
		slices.ContainsFunc(refused.Funcs, func(f IRCensusFunc) bool { return f.Name == "P.Twice" || f.Name == "Twice" }) {
		t.Errorf("refused %+v, want First and not Twice", refused.Funcs)
	}
	if msg := err.Error(); !strings.Contains(msg, "First") || !strings.Contains(msg, "type defined over a non-basic type") {
		t.Errorf("the error does not name First and its stop:\n%s", msg)
	}

	const none = `type P { A int, B int }

func Twice(p P) P {
	P{A: p.A * 2, B: p.B}
}

func main() {
	let _ = Twice(P{A: 1, B: 2})
}
`
	want, rep, err := LowerIR(factsUnit(t, none))
	if err != nil || len(rep.Fallback) != 0 {
		t.Fatalf("the program without stops: %v, fallbacks %v", err, rep.Fallback)
	}
	u = factsUnit(t, none)
	u.NoFallback = true
	files, _, err := LowerIR(u)
	if err != nil {
		t.Fatalf("with the switch a program without stops is refused: %v", err)
	}
	if !slices.EqualFunc(files, want, func(a, b File) bool { return a.Path == b.Path && string(a.Data) == string(b.Data) }) {
		t.Error("the switch changes the lowered files of a program without stops")
	}
}

// TestFallbackOffRefusesCodecFallback verifies that codec functions falling
// back via irState.codecText are refused under Unit.NoFallback, not silently
// left on the text path.
//
// Mutant (testdata/mutants.json): ir-codec-text-bypasses-nofallback.
func TestFallbackOffRefusesCodecFallback(t *testing.T) {
	st := &irState{noFallback: true}
	st.codecText("wireEncode_Test", "codec writer on the reference Writer: synthetic stop")
	err := st.refuseFallbacks()
	var refused *FallbackRefused
	if !errors.As(err, &refused) {
		t.Fatalf("with NoFallback a codecText fallback was not refused: %v", err)
	}
	if !slices.ContainsFunc(refused.Funcs, func(f IRCensusFunc) bool {
		return f.Codec && f.Name == "wireEncode_Test" && slices.Contains(f.Reasons, "codec writer on the reference Writer: synthetic stop")
	}) {
		t.Fatalf("refused funcs %+v missing expected Codec entry", refused.Funcs)
	}
	saved := wireIRLowering
	wireIRLowering = "text"
	defer func() { wireIRLowering = saved }()
	oracle := &irState{noFallback: true}
	oracle.codecText("wireEncode_Test", "codec writer on the reference Writer")
	if err := oracle.refuseFallbacks(); err != nil {
		t.Fatalf("oracle mode wireIRLowering=text was refused: %v", err)
	}
}

// TestFallbackOffAcceptsIntrinsicAndCorpusForms verifies that Unit.NoFallback
// accepts the Go intrinsic (prelude.Sleep) and lowers formerly falling-back
// corpus forms (empty Never match / Absurd, multi-subject match on Map.Get)
// cleanly via IR with zero fallbacks and zero CodecText.
//
// Mutants (testdata/mutants.json): ir-empty-never-match-refused,
// ir-multi-get-match-refused.
func TestFallbackOffAcceptsIntrinsicAndCorpusForms(t *testing.T) {
	const src = `func emptyMatch[T](n Never) T {
	match n {
	}
}

func bothGet(a Map[string, int], b Map[string, int], k string) int {
	match (a.Get(k), b.Get(k)) {
	case (Some[int] as x, Some[int] as y):
		x.Value + y.Value
	case (Some[int] as x, None):
		x.Value
	case (None, Some[int] as y):
		y.Value
	case (None, None):
		0
	}
}

func main() needs Ambient {
	let a = Map[string, int]{"k": 2}
	let b = Map[string, int]{"k": 3}
	let _ = bothGet(a, b, "k")
	if false {
		let f func(Never) int = emptyMatch[int]
		let _ = f
	}
	NewLiveScheduler().Sleep(0s)
}
`
	o := mustLower(t, "nofallback_forms", src)
	u := Unit{
		Package:    o.pkg,
		Files:      []*syntax.File{o.file},
		Sources:    []Source{{Path: testSourcePath, Text: src}},
		NoFallback: true,
	}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatalf("LowerIR(NoFallback=true) refused valid IR+intrinsic program: %v", err)
	}
	if len(rep.Fallback) != 0 || len(rep.CodecText) != 0 {
		t.Fatalf("expected 0 fallbacks and 0 CodecText, got Fallback=%v CodecText=%v", rep.Fallback, rep.CodecText)
	}
	if !slices.ContainsFunc(rep.IR, func(name string) bool { return strings.Contains(name, "bothGet") }) {
		t.Fatalf("bothGet did not lower on IR: %v", rep.IR)
	}
	if !slices.ContainsFunc(rep.IR, func(name string) bool { return strings.Contains(name, "emptyMatch") }) {
		t.Fatalf("emptyMatch did not lower on IR: %v", rep.IR)
	}
}
