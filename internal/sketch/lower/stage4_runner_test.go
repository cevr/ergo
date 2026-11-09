package lower

import (
	"fmt"
	"iter"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Stage 4 (196l, spec D8): the runner's rule over generated test files, and
// the argument text of a refuted property, without the CLI.

// runnerOutcome is one test of a generated file: its source, given the
// test's index, and the report line the rule gives it.
type runnerOutcome struct {
	src string
	// line is the report line's text (eq) or prefix; defect stops the run.
	line   string
	prefix bool
	failed bool
	defect bool
}

// runnerOutcomes are the outcomes a test can have, each in more than one
// position of a body: a pass, a false assert, an escaped typed failure, a
// defect, a passing and a refuted property, and each of these inside a
// with body, and a defect that an exit action raises after a false assert
// (E3a M3: the defect wins, and the run stops).
var runnerOutcomes = map[string]runnerOutcome{
	"pass":        {src: "test T{i} {{ assert {i} == {i} }}", line: "ok T{i}"},
	"assert":      {src: "test T{i} {{ assert {i} == {i}+1 }}", line: "FAIL T{i}: assert failed at main.ergo:", prefix: true, failed: true},
	"fail":        {src: "test T{i} {{ try bad() }}", line: "FAIL T{i}: a failure escaped the test at main.ergo:", prefix: true, failed: true},
	"defect":      {src: "test T{i} {{ assert at([]int{{}}, {i}) == 0 }}", line: "FAIL T{i}: a defect, which stops the run", failed: true, defect: true},
	"ppass":       {src: "test T{i}(v int) {{ assert v == v }}", line: "ok T{i} (100 cases)"},
	"pfail":       {src: "test T{i}(v int, s string) {{ assert v != v }}", line: "FAIL T{i} (case 1 of 100, seed 1, size 0: v = ", prefix: true, failed: true},
	"withAssert":  {src: "test T{i} {{\n\twith Live {{\n\t\tassert {i} > {i}\n\t}}\n}}", line: "FAIL T{i}: assert failed at main.ergo:", prefix: true, failed: true},
	"withFail":    {src: "test T{i} {{\n\twith Live {{\n\t\ttry bad()\n\t}}\n}}", line: "FAIL T{i}: a failure escaped the test at main.ergo:", prefix: true, failed: true},
	"withDefect":  {src: "test T{i} {{\n\twith Live {{\n\t\tassert at([]int{{}}, {i}) == 0\n\t}}\n}}", line: "FAIL T{i}: a defect, which stops the run", failed: true, defect: true},
	"exitDefect":  {src: "test T{i} {{\n\tscope.OnExit() {{\n\t\tassert at([]int{{}}, {i}) == 0\n\t}}\n\tassert false\n}}", line: "FAIL T{i}: a defect, which stops the run", failed: true, defect: true},
	"propertyRow": {src: "test T{i}(n int) {{\n\tif n == n {{\n\t\ttry bad()\n\t}}\n}}", line: "FAIL T{i} (case 1 of 100, seed 1, size 0: n = ", prefix: true, failed: true},
}

const runnerPrelude = "import scope\nimport time\n\ntype Bad {}\n\nfunc bad() fails Bad { Bad }\n\nfunc at(xs []int, i int) int { xs[i] }\n\nlet Live = context { Scheduler: time.NewManual(0) }\n\nfunc cleanup() {\n\tscope.OnExit() {}\n}\n\nfunc main() needs Console { Console.Println(\"MAIN-MUST-NOT-RUN\") }\n"

// runnerModel is the rule: each test runs once in source order; a pass
// prints ok; a failed assert, a typed failure or a refuted property fails
// its test and the run goes on; a defect fails its test and stops the run
// with exit 2; otherwise the run ends with FAIL (k of n tests) and exit 1,
// or PASS (n tests) and exit 0.
func runnerModel(seq []string) (lines []runnerOutcome, exit int) {
	failed := 0
	for i, name := range seq {
		o := runnerOutcomes[name]
		o.line = strings.ReplaceAll(o.line, "{i}", strconv.Itoa(i))
		lines = append(lines, o)
		if o.failed {
			failed++
		}
		if o.defect {
			return lines, 2
		}
	}
	if failed > 0 {
		return append(lines, runnerOutcome{line: fmt.Sprintf("FAIL (%d of %d tests)", failed, len(seq))}), 1
	}
	return append(lines, runnerOutcome{line: fmt.Sprintf("PASS (%d tests)", len(seq))}), 0
}

// runnerSeqs is the domain of TestTestRunnerModel at 0a8e52fd, in its
// order: every sequence of outcomes of length 1 and 2, and 24 seeded
// sequences of length 3 to 6.
func runnerSeqs() [][]string {
	names := make([]string, 0, len(runnerOutcomes))
	for n := range runnerOutcomes {
		names = append(names, n)
	}
	sort.Strings(names)
	var seqs [][]string
	for _, a := range names {
		seqs = append(seqs, []string{a})
		for _, b := range names {
			seqs = append(seqs, []string{a, b})
		}
	}
	rng := rand.New(rand.NewSource(4))
	for k := 0; k < 24; k++ {
		seq := make([]string, 3+rng.Intn(4))
		for i := range seq {
			seq[i] = names[rng.Intn(len(names))]
		}
		seqs = append(seqs, seq)
	}
	return seqs
}

// TestTestRunnerModel builds, through LowerTests, one test file per
// sequence of outcomes (runnerSeqs), runs each test binary and compares
// its report and exit status with the rule (runnerModel). A run that
// fails names its count on standard error; a defect leaves the runtime's
// report; main never runs. The unsplit test took 16.1 s (25.6 s under
// load), more than a shard's target, so the sequences are a split
// (check-split-2, batch 9) with the domain of the unsplit test at
// 0a8e52fd; each part builds its own module. The parts run in the full
// tier only (short 0), as the unsplit test did.
//
// A case is a sequence with its index in the domain: two seeded sequences
// may be equal, and a key names one case.
var testRunnerModel = newSplit(split[runnerSeq]{
	name: "TestTestRunnerModel", parts: 4,
	cases: func(yield func(runnerSeq) bool) {
		for k, seq := range runnerSeqs() {
			if !yield(runnerSeq{k, seq}) {
				return
			}
		}
	},
	key: func(c runnerSeq) string { return fmt.Sprintf("%03d %s", c.k, strings.Join(c.seq, " ")) },
	run: func(t *testing.T, cases iter.Seq[runnerSeq]) {
		var seqs [][]string
		for c := range cases {
			seqs = append(seqs, c.seq)
		}
		runnerModelCheck(t, seqs)
	},
})

// runnerSeq is one case of TestTestRunnerModel.
type runnerSeq struct {
	k   int
	seq []string
}

func TestTestRunnerModelPart0(t *testing.T) { testRunnerModel.Part(t, 0) }
func TestTestRunnerModelPart1(t *testing.T) { testRunnerModel.Part(t, 1) }
func TestTestRunnerModelPart2(t *testing.T) { testRunnerModel.Part(t, 2) }
func TestTestRunnerModelPart3(t *testing.T) { testRunnerModel.Part(t, 3) }

// runnerModelCheck builds and runs the test file of each sequence and
// compares it with the rule.
//
// It builds Go because it runs the emitted test harness under go test: the
// harness's verdicts and the values it prints are the output; no in-process
// check observes that, and Ergo has no reference interpreter yet (test-speed
// T8).
func runnerModelCheck(t *testing.T, seqs [][]string) {
	t.Helper()
	var builds []*lowered
	for k, seq := range seqs {
		var src strings.Builder
		src.WriteString(runnerPrelude)
		for i, name := range seq {
			src.WriteString("\n" + strings.NewReplacer("{i}", strconv.Itoa(i), "{{", "{", "}}", "}").Replace(runnerOutcomes[name].src) + "\n")
		}
		builds = append(builds, lowerTestBuild(t, fmt.Sprintf("seq%03d", k), src.String()))
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	bad := 0
	for k, seq := range seqs {
		stdout, stderr, code := runExe(t, exes[fmt.Sprintf("seq%03d", k)])
		want, exit := runnerModel(seq)
		got := lines(stdout)
		okay := code == exit && len(got) == len(want) && !strings.Contains(stdout+stderr, "MAIN-MUST-NOT-RUN")
		for i := 0; okay && i < len(want); i++ {
			if want[i].prefix {
				okay = strings.HasPrefix(got[i], want[i].line)
			} else {
				okay = got[i] == want[i].line
			}
		}
		switch {
		case exit == 1 && !strings.Contains(stderr, "tests failed"):
			okay = false
		case exit == 2 && !strings.Contains(stderr, "panic: "):
			okay = false
		}
		if !okay {
			bad++
			if bad <= 3 {
				var w []string
				for _, o := range want {
					w = append(w, o.line)
				}
				t.Errorf("%v: exit %d (want %d)\nstdout:\n%s\nwant:\n%s\nstderr:\n%s", seq, code, exit, stdout, strings.Join(w, "\n"), firstLines(stderr, 8))
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d generated test files disagree with the rule", bad, len(seqs))
	}
}

// literalKind is one parameter kind of TestArgumentTextRoundTrips: its
// type and the Ergo expression that tells whether v and w are equal.
type literalKind struct{ name, typ, eq string }

var literalKinds = []literalKind{
	{"I8", "int8", "v == w"}, {"I16", "int16", "v == w"}, {"I32", "int32", "v == w"}, {"I64", "int64", "v == w"},
	{"Int", "int", "v == w"}, {"U8", "uint8", "v == w"}, {"U16", "uint16", "v == w"}, {"U32", "uint32", "v == w"},
	{"U64", "uint64", "v == w"}, {"Uint", "uint", "v == w"}, {"Uptr", "uintptr", "v == w"},
	{"F32", "float32", "v == w"}, {"F64", "float64", "v == w"}, {"Bool", "bool", "v == w"},
	{"Str", "string", "v == w"}, {"Dur", "duration", "v == w"},
	{"Ints", "[]int", "eqSlice(v, w)"}, {"Strs", "[]string", "eqSlice(v, w)"}, {"Points", "[]Point", "eqSlice(v, w)"},
	{"MapSI", "Map[string, int]", "eqMap(v, w)"}, {"MapIB", "Map[int, bool]", "eqMap(v, w)"},
	{"MapFS", "Map[float64, string]", "eqMap(v, w)"}, {"MapShape", "Map[Shape, int]", "eqMap(v, w)"},
	{"Tup", "(int, string)", "v == w"}, {"Nest", "((int, bool), Point)", "v == w"},
	{"Pt", "Point", "v == w"}, {"Wrap", "Wrap", "eqSlice(v.Items, w.Items) && v.Tag == w.Tag"},
	{"Empty", "Empty", "v == w"}, {"Shape", "Shape", "v == w"}, {"Tree", "Tree", "v == w"},
	{"Box", "Box[int]", "v == w"}, {"Maybe", "Maybe[string]", "v == w"}, {"MaybeBox", "Maybe[Box[Shape]]", "v == w"},
	{"Cel", "Celsius", "v == w"}, {"Ids", "Ids", "eqIds(v, w)"},
}

const literalTypes = `type Point { X int, Y float64 }

type Wrap { Items []Point, Tag string }

type Empty {}

type Shape = Dot{} | Circle{R float64} | Box2{W int8, H uint16}

type Tree = Leaf{} | Node{L Tree, R Tree, N int}

type Box[T] { V T }

type Celsius float64

type Ids []int

type Just[T] { V T }

type Nothing {}

type Maybe[T] = Just[T] | Nothing

func eqSlice[T comparable](a []T, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x in a {
		if x != b[i] {
			return false
		}
	}
	true
}

func eqIds(a Ids, b Ids) bool {
	if len(a) != len(b) {
		return false
	}
	for i, x in a {
		if x != b[i] {
			return false
		}
	}
	true
}

func eqMap[K comparable, V comparable](a Map[K, V], b Map[K, V]) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x in a {
		match b.Get(k) {
		case Some[V] as s:
			if s.Value != x {
				return false
			}
		case None:
			return false
		}
	}
	true
}
`

// TestArgumentTextRoundTrips: for every parameter kind the generator
// draws (complex aside: Ergo has no complex literal), at testDepth, the
// text a refuted property prints for its argument checks as a literal of
// its type and gives an equal value. A first file refutes a property per
// kind at its first case under seeds 1 to 12, so each run prints one
// argument per kind. A second file per seed binds each printed text with
// `let w T = text` in a property of the same name, which draws the same
// first argument under that seed, and asserts that they differ: it must
// fail at case 1 for every kind, so the text gave an equal value. The
// second files must check with no diagnostic. Map keys print sorted.
// Durations also round-trip from values that reach every unit, h to ns
// (durationTextRoundTrips).
//
// It builds Go because it runs the emitted test harness under go test: the
// harness's verdicts and the values it prints are the output; no in-process
// check observes that, and Ergo has no reference interpreter yet (test-speed
// T8).
func TestArgumentTextRoundTrips(t *testing.T) {
	needsBuild(t)
	const seeds = 12
	var first strings.Builder
	first.WriteString(literalTypes)
	for _, k := range literalKinds {
		first.WriteString("\ntest K" + k.name + "(v " + k.typ + ") { assert false }\n")
	}
	firstProg := lowerTestBuild(t, "first", first.String())
	durationTextRoundTrips(t, firstProg)
	exes := buildAll(t, []*lowered{firstProg}, true)
	if exes == nil {
		return
	}
	line := regexp.MustCompile(`(?m)^FAIL K(\w+) \(case 1 of 100, seed (\d+), size 0: v = (.*)\): assert failed at main\.ergo:\d+:\d+: false$`)
	texts := make([]map[string]string, seeds+1)
	for s := 1; s <= seeds; s++ {
		stdout, stderr, code := runExeArgs(t, exes["first"], "--seed", strconv.Itoa(s))
		texts[s] = map[string]string{}
		for _, m := range line.FindAllStringSubmatch(stdout, -1) {
			if m[2] == strconv.Itoa(s) {
				texts[s][m[1]] = m[3]
			}
		}
		if code != 1 || len(texts[s]) != len(literalKinds) {
			t.Fatalf("seed %d: exit %d, %d of %d kinds printed an argument:\n%s\n%s", s, code, len(texts[s]), len(literalKinds), firstLines(stdout, 40), firstLines(stderr, 8))
		}
		if keys := mapKeys(texts[s]["MapIB"]); !sort.SliceIsSorted(keys, func(i, j int) bool { return keys[i] < keys[j] }) {
			t.Errorf("seed %d: the keys of %s are not sorted", s, texts[s]["MapIB"])
		}
		for _, f := range []string{"F32", "F64"} {
			if !strings.ContainsAny(texts[s][f], ".e") {
				t.Errorf("seed %d: the float %s has no decimal point or exponent", s, texts[s][f])
			}
		}
	}
	var second []*lowered
	for s := 1; s <= seeds; s++ {
		var src strings.Builder
		src.WriteString(literalTypes)
		for _, k := range literalKinds {
			fmt.Fprintf(&src, "\ntest K%s(v %s) {\n\tlet w %s = %s\n\tassert !(%s)\n}\n", k.name, k.typ, k.typ, texts[s][k.name], k.eq)
		}
		file, err := syntax.Parse(src.String())
		if err != nil {
			t.Fatalf("seed %d: the printed literals do not parse: %v\n%s", s, err, src.String())
		}
		if _, diags := check.Check("main", []*syntax.File{file}, nil); len(diags) > 0 {
			t.Fatalf("seed %d: the printed literals do not check: %v\n%s", s, diags, src.String())
		}
		second = append(second, lowerTestBuild(t, "second"+strconv.Itoa(s), src.String()))
	}
	exes = buildAll(t, second, true)
	if exes == nil {
		return
	}
	for s := 1; s <= seeds; s++ {
		stdout, _, _ := runExeArgs(t, exes["second"+strconv.Itoa(s)], "--seed", strconv.Itoa(s))
		for _, k := range literalKinds {
			if !strings.Contains(stdout, "FAIL K"+k.name+" (case 1 of 100, seed "+strconv.Itoa(s)+", size 0: ") {
				t.Errorf("seed %d: %s: the literal %s did not give the value it was printed from", s, k.name, texts[s][k.name])
			}
		}
	}
}

// durationUnits are the duration units, largest first, with their sizes.
var durationUnits = []struct {
	suffix string
	size   int64
}{{"h", 3600e9}, {"m", 60e9}, {"s", 1e9}, {"ms", 1e6}, {"us", 1e3}, {"ns", 1}}

// durationValues draws whole multiples of each unit, h to ns, small and
// large, with both signs; mixed values, a whole number of one unit plus a
// part of the next smaller one; and the edges 0, 1500ms, the largest and
// the smallest duration. The product generator draws a whole number of a
// unit above ns about once in 1000 draws (S4a round 1 M1), so the round
// trip of the product's draws alone does not reach the other units.
func durationValues(rng *rand.Rand) []int64 {
	var out []int64
	for i, u := range durationUnits {
		most := int64(math.MaxInt64) / u.size
		for j := 0; j < 6; j++ {
			n := 1 + rng.Int63n(min(most, 100))
			if j >= 3 {
				n = 1 + rng.Int63n(most)
			}
			if j%2 == 1 {
				n = -n
			}
			out = append(out, n*u.size)
		}
		if i+1 < len(durationUnits) {
			small := durationUnits[i+1].size
			out = append(out, (1+rng.Int63n(50))*u.size+(1+rng.Int63n(u.size/small-1))*small)
		}
	}
	return append(out, 0, 1500e6, math.MaxInt64, math.MinInt64)
}

// durationUnit is the largest unit of which n is a whole number: the unit
// the argument text must print n in (runtime.md, test runner rule 5).
func durationUnit(n int64) string {
	for _, u := range durationUnits {
		if n != 0 && n%u.size == 0 {
			return u.suffix
		}
	}
	return "ns"
}

// durationTextRoundTrips is the duration leg of TestArgumentTextRoundTrips.
// A Go test in prog's emitted module prints durationValues through the
// emitted duration text helper. Each text must name the largest exact unit,
// the values must reach every unit, and a test file that binds each text
// with `let w duration = text` must check and find it equal to the value
// written in nanoseconds.
//
// It builds Go because it runs the emitted test harness under go test: the
// harness's verdicts and the values it prints are the output; no in-process
// check observes that, and Ergo has no reference interpreter yet (test-speed
// T8).
func durationTextRoundTrips(t *testing.T, prog *lowered) {
	t.Helper()
	values := durationValues(rand.New(rand.NewSource(1500)))
	lits := make([]string, len(values))
	for i, v := range values {
		lits[i] = strconv.FormatInt(v, 10)
	}
	out := filepath.Join(t.TempDir(), "durations.txt")
	driver := "package main\n\nimport (\n\t\"os\"\n\t\"strings\"\n\t\"testing\"\n)\n\n" +
		"func TestDurationText(t *testing.T) {\n\tvar b strings.Builder\n\tfor _, v := range []int64{" + strings.Join(lits, ", ") + "} {\n\t\tb.WriteString(ergoLitDuration(v) + \"\\n\")\n\t}\n" +
		"\tif err := os.WriteFile(os.Getenv(\"ERGO_DURATION_TEXT\"), []byte(b.String()), 0o644); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n"
	goTestEmitted(t, prog, driver, "ERGO_DURATION_TEXT="+out)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	texts := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(texts) != len(values) {
		t.Fatalf("%d duration texts for %d values", len(texts), len(values))
	}
	reached := map[string]bool{}
	shape := regexp.MustCompile(`^-?[0-9]+(h|m|s|ms|us|ns)$`)
	var src strings.Builder
	for i, v := range values {
		want := lits[i] + "ns"
		if v == math.MinInt64 {
			want = "-9223372036854775807ns - 1ns"
		} else if m := shape.FindStringSubmatch(texts[i]); m == nil || m[1] != durationUnit(v) {
			t.Errorf("the duration %dns prints as %q, not in its largest exact unit %s", v, texts[i], durationUnit(v))
		}
		reached[durationUnit(v)] = true
		fmt.Fprintf(&src, "test D%d() {\n\tlet w duration = %s\n\tassert w == %s\n}\n\n", i, texts[i], want)
	}
	for _, u := range durationUnits {
		if !reached[u.suffix] {
			t.Errorf("no drawn duration is a whole number of %s", u.suffix)
		}
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "durations", src.String())}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["durations"])
	if code != 0 || !strings.HasSuffix(stdout, fmt.Sprintf("PASS (%d tests)\n", len(values))) {
		t.Errorf("the printed durations do not give their values back: exit %d\n%s\n%s\n%s", code, firstLines(stdout, 40), firstLines(stderr, 8), src.String())
	}
}

// mapKeys are the integer keys of a printed Map[int, bool], in order.
func mapKeys(text string) []int {
	var keys []int
	for _, m := range regexp.MustCompile(`(-?\d+): (?:true|false)`).FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		keys = append(keys, n)
	}
	return keys
}
