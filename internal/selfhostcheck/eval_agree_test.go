package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

type evalDumpOutcome struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Reason string `json:"reason"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Col    int    `json:"col"`
	Steps  int64  `json:"steps"`
	Value  string `json:"value"`
}

type evalDumpStats struct {
	TotalSteps  uint64 `json:"total_steps"`
	BuiltinsHit uint64 `json:"builtins_hit"`
}

type evalCaseFile struct {
	Name string
	Src  string
}

type evalPkgUnit struct {
	ImportPath string
	Files      []evalCaseFile
}

type evalCase struct {
	Name        string
	Pkgs        []evalPkgUnit // single package unless multi-package; root package is last
	Steps       int64         // 0 => default 1_000_000
	Depth       int           // 0 => default 256
	Cases       int           // 0 => default 100
	HasEvalMain bool
}

type evalMapImporter struct {
	pkgs map[string]*check.Package
}

func (m evalMapImporter) Import(path string) (*check.Package, error) {
	if p, ok := m.pkgs[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

func evalDumpBin(t *testing.T, ctx context.Context) string {
	t.Helper()
	cli := ergoCLI(t, ctx)
	bin := filepath.Join(t.TempDir(), "evaldump")
	out, err := runCompiler(t, ctx, cli, "build", "-o", bin, "./cmd/evaldump")
	if err != nil {
		t.Fatalf("build evaldump: %v\n%s", err, out)
	}
	return bin
}

func checkEvalCaseUnits(t *testing.T, c evalCase, tmpDir string) ([]string, *check.Package, bool) {
	t.Helper()
	imp := evalMapImporter{pkgs: map[string]*check.Package{}}
	var manifestLines []string
	var rootPkg *check.Package
	for ui, u := range c.Pkgs {
		pkgDir := filepath.Join(tmpDir, fmt.Sprintf("u%d", ui))
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		var synFiles []*syntax.File
		var filePaths []string
		for _, cf := range u.Files {
			fp := filepath.Join(pkgDir, cf.Name)
			if err := os.WriteFile(fp, []byte(cf.Src), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := syntax.Parse(cf.Src)
			if err != nil {
				t.Errorf("case %s (%s/%s): parse: %v\n%s", c.Name, u.ImportPath, cf.Name, err, cf.Src)
				return nil, nil, false
			}
			f.Name = cf.Name
			synFiles = append(synFiles, f)
			filePaths = append(filePaths, fp)
		}
		pkg, diags := check.Check(u.ImportPath, synFiles, imp)
		if len(diags) > 0 {
			t.Errorf("case %s (%s): check diagnostics: %v", c.Name, u.ImportPath, diags)
			return nil, nil, false
		}
		imp.pkgs[u.ImportPath] = pkg
		rootPkg = pkg
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		ef := filepath.Join(pkgDir, "ext")
		ff := filepath.Join(pkgDir, "foreign")
		writeLines(t, ef, ext)
		writeLines(t, ff, foreign)
		manifestLines = append(manifestLines, strings.Join(append([]string{u.ImportPath, ef, ff}, filePaths...), " "))
	}
	return manifestLines, rootPkg, true
}

func parseEvalDumpOutput(t *testing.T, lines []string) (map[int][]evalDumpOutcome, map[int]evalDumpStats) {
	t.Helper()
	byPkg := map[int][]evalDumpOutcome{}
	statsByPkg := map[int]evalDumpStats{}
	cur := -1
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "@pkg ") {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "@pkg ")))
			if err != nil {
				t.Fatalf("bad @pkg line %q: %v", l, err)
			}
			cur = n
			continue
		}
		if strings.HasPrefix(l, "@stats ") {
			var st evalDumpStats
			if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "@stats ")), &st); err != nil {
				t.Fatalf("bad @stats line %q: %v", l, err)
			}
			statsByPkg[cur] = st
			continue
		}
		var out evalDumpOutcome
		if err := json.Unmarshal([]byte(l), &out); err != nil {
			t.Fatalf("bad outcome json line %q: %v", l, err)
		}
		byPkg[cur] = append(byPkg[cur], out)
	}
	return byPkg, statsByPkg
}

func normalizeGoResultName(name string) (kind string, norm string) {
	if strings.HasPrefix(name, "law ") {
		return "law", strings.TrimPrefix(name, "law ")
	}
	return "test", name
}

func singlePkgCase(name, src string, hasEvalMain bool) evalCase {
	return evalCase{
		Name:        name,
		Pkgs:        []evalPkgUnit{{ImportPath: "case/" + name, Files: []evalCaseFile{{Name: "main.ergo", Src: src}}}},
		HasEvalMain: hasEvalMain,
	}
}

func evalParityCorpus() []evalCase {
	return []evalCase{
		singlePkgCase("literals_widths_and_interp", `
func EvalMain() string {
	let a int8 = -12
	let b int16 = 320
	let c int32 = -70000
	let d int64 = 9000000000
	let e uint8 = 250
	let f uint16 = 65000
	let g uint32 = 4000000000
	let h uint64 = 18000000000
	let b1 byte = 65
	let r1 rune = 'λ'
	let fl float32 = 1.5
	let fd float64 = 2.25
	let ok bool = true
	let raw = `+"`raw\\txt`"+`
	let d1 = 1s + 500ms
	"\(a):\(b):\(c):\(d):\(e):\(f):\(g):\(h):\(b1):\(r1):\(fl):\(fd):\(ok):\(raw):\(int64(d1))"
}

test LiteralsWidths {
	let x int8 = 100
	assert x == int8(100)
	let d = 2s + 250ms
	assert d == 2250ms
	assert int64(d) == 2250000000
}
`, true),

		singlePkgCase("signed_unsigned_wrap_and_shift", `
func wrapId[T](x T) T { x }

func EvalMain() string {
	let one8 = wrapId[int8](1)
	let minus1 = wrapId[int8](-1)
	let mut a = wrapId[int8](127)
	a = a + one8
	let mut b = wrapId[int8](-128)
	b = b - one8
	let min8 = wrapId[int8](-128)
	let c = min8 / minus1
	let d = min8 % minus1
	let e = wrapId[int8](-8) >> wrapId[uint8](1)
	let f = wrapId[uint8](248) >> wrapId[uint8](1)
	let g = wrapId[uint8](255) &^ wrapId[uint8](0x0f)
	let h = ^wrapId[uint8](0)
	"\(a):\(b):\(c):\(d):\(e):\(f):\(g):\(h)"
}

test IntBitwidthWrap {
	let max8 = wrapId[int8](127)
	let min8 = wrapId[int8](-128)
	let one8 = wrapId[int8](1)
	let minus1 = wrapId[int8](-1)
	let max16 = wrapId[uint16](65535)
	let two16 = wrapId[uint16](2)
	assert max8+one8 == int8(-128)
	assert min8-one8 == int8(127)
	assert min8/minus1 == int8(-128)
	assert min8%minus1 == int8(0)
	assert max16+two16 == uint16(1)
	assert wrapId[int8](-8)>>1 == int8(-4)
	assert wrapId[uint8](248)>>1 == uint8(124)
}
`, true),

		singlePkgCase("float_ieee_nan_shortcircuit", `
func divZero() int {
	let z = zeroInt()
	1 / z
}

func zeroInt() int { 0 }
func zeroF64() float64 { 0.0 }

func EvalMain() string {
	let a = float32(0.25) + float32(0.5)
	let z = zeroF64()
	let b = z / z
	let eq = b == b
	let neq = b != b
	let sc1 = false && (divZero() == 0)
	let sc2 = true || (divZero() == 0)
	"\(a):\(eq):\(neq):\(sc1):\(sc2)"
}

test FloatAndShortCircuit {
	let zero = zeroF64()
	let nan = zero / zero
	assert !(nan == nan)
	assert nan != nan
	assert !(nan < 1.0)
	assert !(nan > 1.0)
	assert !(false && (divZero() == 1))
	assert true || (divZero() == 1)
}
`, true),

		singlePkgCase("defect_div_zero", `
func z0() int { 0 }

test DivZero {
	let v = 10 / z0()
	assert v == 0
}
`, false),

		singlePkgCase("defect_neg_shift", `
func negShift() int { -1 }

test NegShift {
	let v = 1 << negShift()
	assert v == 0
}
`, false),

		singlePkgCase("defect_slice_oob", `
test SliceOOB {
	let xs = []int{1, 2, 3}
	assert xs[5] == 0
}
`, false),

		singlePkgCase("defect_slice_bounds", `
func loIdx() int { 3 }

test SliceBounds {
	let xs = []int{1, 2, 3, 4}
	let ys = xs[loIdx():1]
	assert ys.Len() == 0
}
`, false),

		singlePkgCase("defect_mod_zero", `
func z0() int { 0 }

test ModZero {
	let v = 10 % z0()
	assert v == 0
}
`, false),

		singlePkgCase("defect_str_slice_oob", `
test StrSliceOOB {
	let s = "abc"
	let sub = s[1:10]
	assert sub.Len() == 0
}
`, false),

		singlePkgCase("refuted_assert", `
test AssertFails {
	let x = 2 + 2
	assert x == 5
}
`, false),

		singlePkgCase("skipped_obligation", `
test SkippedCase {
	skip "not needed"
	assert false
}
`, false),

		singlePkgCase("defect_stops_following", `
func zZero() int { 0 }

test FirstPasses {
	assert 1 + 1 == 2
}

test SecondDefects {
	assert (1 / zZero()) == 0
}

test ThirdNotRun {
	assert true
}

law FourthLawNotRun() { true }
`, false),

		singlePkgCase("cow_nested_place_assign", `
type Leaf {
	Count int,
	Tag   string,
}

type Holder {
	Items []Leaf,
	Total int,
}

func EvalMain() string {
	let mut h = Holder{
		Items: []Leaf{{Count: 1, Tag: "a"}, {Count: 2, Tag: "b"}},
		Total: 3,
	}
	let snap = h
	h.Items[1].Count += 10
	h.Items[0].Tag = "z"
	h.Total *= 4
	"\(snap.Items[1].Count):\(snap.Items[0].Tag):\(snap.Total):\(h.Items[1].Count):\(h.Items[0].Tag):\(h.Total)"
}

test COWNestedPlaceIsolation {
	let mut h = Holder{
		Items: []Leaf{{Count: 5, Tag: "x"}, {Count: 7, Tag: "y"}},
		Total: 12,
	}
	let saved = h
	h.Items[0].Count += 20
	h.Total -= 2
	assert saved.Items[0].Count == 5
	assert saved.Total == 12
	assert h.Items[0].Count == 25
	assert h.Total == 10
}
`, true),

		singlePkgCase("control_flow_range_break_continue", `
func sumMatrix() int {
	let mut total = 0
	for i in 0..6 {
		if i == 1 {
			continue
		}
		if i == 5 {
			break
		}
		for j in 0..4 {
			if j == 2 {
				continue
			}
			total = total + (i*10 + j)
		}
	}
	total
}

func EvalMain() string {
	let mut s = 0
	let mut k = 0
	for k < 5 {
		k += 1
		s += k
	}
	let xs = []int{10, 20, 30}
	let mut xsum = 0
	for idx, v in xs {
		xsum += idx + v
	}
	let mut runes = 0
	for _, r in "ab" {
		runes += int(r)
	}
	"\(sumMatrix()):\(s):\(xsum):\(runes)"
}

test ControlFlow {
	assert sumMatrix() == 324
}
`, true),

		singlePkgCase("match_value_and_union_patterns", `
type Circle { R int, }
type Rect { W int, H int, }
type Dot {}
type Shape = Circle | Rect | Dot

func area(s Shape) int {
	match s {
	case Circle{R: r}:
		r * r * 3
	case Rect{W: w, H: h}:
		w * h
	case Dot:
		1
	}
}

func classify(n int) string {
	match n {
	case 0:
		"zero"
	case 1, 2:
		"small"
	case _:
		"big"
	}
}

func EvalMain() string {
	let a = area(Circle{R: 3}) + area(Rect{W: 4, H: 5}) + area(Dot)
	let b = classify(0) + "," + classify(2) + "," + classify(9)
	let (x, y) = (11, 22)
	"\(a):\(b):\(x+y)"
}

test MatchPatterns {
	assert area(Circle{R: 2}) == 12
	assert area(Rect{W: 3, H: 7}) == 21
	assert area(Dot) == 1
	assert classify(1) == "small"
}
`, true),

		singlePkgCase("struct_spread_and_zero_values", `
type Config {
	Host string,
	Port int,
	TLS  bool,
}

func EvalMain() string {
	let zeroI = int(0)
	let mut zeroS string
	let mut zeroB bool
	zeroS = zeroS + ""
	zeroB = zeroB || false
	let base = Config{Host: "localhost", Port: 8080, TLS: false}
	let next = Config{..base, Port: 8443, TLS: true}
	"\(zeroI):\(zeroS):\(zeroB)|\(base.Port)|\(next.Host):\(next.Port):\(next.TLS)"
}

test StructSpread {
	let a = Config{Host: "h", Port: 1, TLS: true}
	let b = Config{..a, Port: 2}
	assert b.Host == "h" && b.Port == 2 && b.TLS
}
`, true),

		singlePkgCase("slices_ops_and_3index_cow", `
func EvalMain() string {
	let mut a = []int{1, 2, 3, 4}
	let b = a.Slice(1, 3)
	let c = a[0:2:2]
	let d = c.Append(99)
	let e = a.Concat([]int{5, 6})
	let (taken, rest) = e.TakeAt(1)
	let clones = a.Clone()
	a[0] = 777
	"\(a[0]):\(b[0]):\(b[1]):\(d[2]):\(taken):\(rest.Len()):\(clones[0])"
}

test SliceCOW {
	let xs = []int{1, 2, 3, 4}
	let head = xs[0:2:2]
	let mut ys = head.Append(9)
	ys[0] = 100
	assert xs[0] == 1
	assert xs[2] == 3
	assert ys[0] == 100 && ys[2] == 9
}
`, true),

		singlePkgCase("maps_ops_and_sorted_iter", `
import option

func unwrapOr(o option.Option[int], d int) int {
	match o {
	case option.Some{Value: v}: v
	case option.None: d
	}
}

func EvalMain() string {
	let mut m = Map[string, int]{"b": 2, "a": 1, "c": 3}
	let snap = m
	m = m.Put("b", 20)
	m = m.Delete("a")
	let (oldOpt, m2) = m.Take("c")
	let mut keys = ""
	for k, v in m2 {
		keys = keys + "\(k)=\(v);"
	}
	let g = unwrapOr(snap.Get("a"), -1)
	"\(snap.Len()):\(m.Len()):\(m2.Len()):\(unwrapOr(oldOpt, -1)):\(keys):\(g)"
}

test MapCOWAndOrder {
	let mut m = Map[string, int]{"z": 1, "a": 2, "m": 3}
	let orig = m
	m = m.Put("a", 99)
	let mut sum = 0
	for _, v in m {
		sum += v
	}
	assert sum == 103
	assert unwrapOr(orig.Get("a"), 0) == 2
}
`, true),

		singlePkgCase("map_order_sensitive_skips", `
test MapOrderSensitiveSkips {
	let m = Map[string, int]{"z": 1, "a": 2, "m": 3}
	let mut out = ""
	for k, v in m {
		out = out + "\(k)\(v)"
	}
	assert out == "a2m3z1"
}
`, false),

		singlePkgCase("generics_methods_and_ufcs", `
type Box[T] {
	Val T,
}

func MapVal[T, U](b Box[T], f func(T) U) Box[U] {
	Box[U]{Val: f(b.Val)}
}

func identity[T](v T) T { v }

func EvalMain() string {
	let b = Box[int]{Val: 21}
	let doubled = b.MapVal(func(x int) int { x * 2 })
	let s = identity[string]("ok")
	"\(doubled.Val):\(s)"
}

test GenericsAndMethods {
	let b = Box[int]{Val: 7}
	let out = b.MapVal(func(x int) int { x + 3 })
	assert out.Val == 10
}
`, true),

		singlePkgCase("refinement_where_parse", `
type Pos int where it > 0

func EvalMain() string {
	let a = ParsePos(5) catch {
	case Unsatisfied: Pos(1)
	}
	let b = ParsePos(-3) catch {
	case Unsatisfied: Pos(99)
	}
	"\(int(a)):\(int(b))"
}

test RefinedParse {
	let p = ParsePos(7) catch {
	case Unsatisfied: Pos(1)
	}
	assert int(p) == 7
}
`, true),

		singlePkgCase("recursion_and_closures_loopcapture", `
func isEven(n int) bool {
	if n == 0 { true } else { isOdd(n - 1) }
}

func isOdd(n int) bool {
	if n == 0 { false } else { isEven(n - 1) }
}

func applyOnce(f func()) { f() }

func makeAdder(start int) func(int) int {
	func(dx int) int {
		start + dx
	}
}

func EvalMain() string {
	let add10 = makeAdder(10)
	let x1 = add10(1)
	let x2 = add10(2)
	let mut acc = 5
	applyOnce(func() { acc += 7 })
	let mut fns = []func() int{}
	for i in 0..5 {
		fns = fns.Append(func() int { i * 10 })
	}
	let mut res = ""
	for idx, f in fns {
		if idx > 0 { res = res + "," }
		res = res + "\(f())"
	}
	"\(isEven(10)):\(isOdd(9)):\(x1):\(x2):\(acc):\(res)"
}

test LoopClosureCapture {
	let mut fns = []func() int{}
	for i in 0..4 {
		fns = fns.Append(func() int { i })
	}
	assert fns[0]() == 0
	assert fns[1]() == 1
	assert fns[2]() == 2
	assert fns[3]() == 3
}
`, true),

		singlePkgCase("try_catch_and_ownfailure_escape", `
type BadDiv { Reason string, }
type BadInput { Code int, }

func safeDiv(a int, b int) int fails BadDiv {
	if b == 0 {
		return BadDiv{Reason: "zero"}
	}
	a / b
}

func step(x int) int fails BadDiv | BadInput {
	if x < 0 {
		return BadInput{Code: x}
	}
	try safeDiv(100, x)
}

func partialCatch(x int) int fails BadInput {
	try step(x) catch {
	case BadDiv:
		-1
	}
}

func outerHandlesNestedTry(flag bool) int fails BadDiv {
	let _ = (if flag { try safeDiv(10, 0) } else { safeDiv(20, 4) }) catch {
	case BadDiv:
		999
	}
	0
}

func EvalMain() string {
	let a = partialCatch(0) catch {
	case BadInput: -9
	}
	let b = partialCatch(-5) catch {
	case BadInput as e: e.Code
	}
	let c = outerHandlesNestedTry(true) catch {
	case BadDiv: 42
	}
	let d = outerHandlesNestedTry(false) catch {
	case BadDiv: 42
	}
	"\(a):\(b):\(c):\(d)"
}

test TryCatchEscape {
	let r = outerHandlesNestedTry(true) catch {
	case BadDiv: 42
	}
	assert r == 42
}
`, true),

		singlePkgCase("escaped_failure_refutes_test", `
type Boom { Msg string, }

func doFail() int fails Boom {
	Boom{Msg: "fail"}
}

test EscapedFailureRefutes {
	let v = try doFail()
	assert v == 1
}
`, false),

		singlePkgCase("catch_never_catches_defect", `
type Unused { Code int, }

func zZero() int { 0 }

func boom() int fails Unused {
	1 / zZero()
}

test CatchDoesNotCatchDefect {
	let v = boom() catch {
	case Unused: 1
	}
	assert v == 1
}
`, false),

		singlePkgCase("stdlib_strings_slices_strconv", `
import slices
import strconv
import strings

func EvalMain() string {
	let parts = strings.Split(strings.TrimSpace("  b,a,c  "), ",")
	let sorted = slices.Sort(parts)
	let joined = strings.Join(sorted, "-")
	let mut b = strings.NewBuilder()
	b.WriteString(joined)
	b.WriteByte(byte('!'))
	let n = strconv.ParseInt("123", 10, 64) catch {
	case _: 0
	}
	let ok = strconv.ParseBool("true") catch {
	case _: false
	}
	let xs = []int{3, 1, 4, 1, 5}
	let ys = slices.Filter(slices.Transform(xs, func(v int) int { v * 2 }), func(v int) bool { v > 4 })
	"\(b.String()):\(strings.ToUpper("ergo")):\(strconv.Itoa(int(n)+1)):\(ok):\(ys.Len()):\(ys[0])"
}

test StdStringsSlicesStrconv {
	assert strings.Contains("abcdef", "cd")
	assert strings.ReplaceAll("foo-bar-foo", "foo", "x") == "x-bar-x"
	assert slices.Equal([]int{1, 2}, []int{1, 2})
}
`, true),

		singlePkgCase("stdlib_math_bits_hex_utf8_cmp_option", `
import bits
import cmp
import hex
import math
import option
import utf8

func EvalMain() string {
	let sq = math.Sqrt(16.0)
	let fl = math.Floor(3.7)
	let oc = bits.OnesCount64(uint64(0xf0f))
	let hs = hex.Encode("hi")
	let rc = utf8.RuneCount("hém")
	let c = cmp.Compare(10, 20)
	let opt = option.MapOr(option.Some[int]{Value: 21}, 0, func(x int) int { x * 2 })
	"\(sq):\(fl):\(oc):\(hs):\(rc):\(c):\(opt)"
}

test StdMathBitsHexUtf8CmpOption {
	assert bits.TrailingZeros64(uint64(8)) == 3
	assert math.Abs(-5.5) == 5.5
	let dec = hex.Decode("6f6b") catch {
	case _: ""
	}
	assert dec == "ok"
}
`, true),

		singlePkgCase("laws_bool_and_equation", `
func add(a int, b int) int { a + b }

law DeMorgan(a bool, b bool) { !(a && b) == (!a || !b) }

law ZeroArgTruth() { add(2, 3) == 5 }

law AddZeroEq(x int) { add(x, 0) == x }
`, false),

		singlePkgCase("laws_refuted_zero_arg", `
law FalseZeroArg() { 1 + 1 == 3 }
`, false),

		singlePkgCase("laws_refuted_bool_param", `
law BadBoolLaw(a bool, b bool) { !(a && !b) }
`, false),

		singlePkgCase("budget_steps_exhausted", `
test InfiniteLoop {
	let mut x = 0
	for {
		x += 1
	}
	assert x == 0
}
`, false),

		singlePkgCase("budget_depth_exhausted", `
func deep(n int) int {
	if n <= 0 { 0 } else { 1 + deep(n - 1) }
}

test DeepRec {
	assert deep(200) == 200
}
`, false),

		{
			Name: "multi_pkg_import",
			Pkgs: []evalPkgUnit{
				{
					ImportPath: "case/lib",
					Files: []evalCaseFile{{
						Name: "lib.ergo",
						Src: `
let Offset = 100

type Counter {
	N int,
}

func Value(c Counter) int { c.N + Offset }

func Double(x int) int { x * 2 }
`,
					}},
				},
				{
					ImportPath: "case/app",
					Files: []evalCaseFile{{
						Name: "app.ergo",
						Src: `
import case/lib

func EvalMain() string {
	let c = lib.Counter{N: 7}
	"\(c.Value()):\(lib.Double(9)):\(lib.Offset)"
}

test MultiPkgCall {
	let c = lib.Counter{N: 5}
	assert c.Value() == 105
	assert lib.Double(8) == 16
}
`,
					}},
				},
			},
			HasEvalMain: true,
		},
	}
}

func runEvalCaseParity(t *testing.T, ctx context.Context, bin string, cases []evalCase, each bool, budgetSteps int64, budgetDepth int) {
	t.Helper()
	tmp := t.TempDir()
	var manifest []string
	rootPkgs := make([]*check.Package, len(cases))
	allValid := true
	for i, c := range cases {
		cDir := filepath.Join(tmp, fmt.Sprintf("case_%d_%s", i, c.Name))
		if err := os.MkdirAll(cDir, 0o755); err != nil {
			t.Fatal(err)
		}
		lines, rootPkg, ok := checkEvalCaseUnits(t, c, cDir)
		if !ok {
			allValid = false
			continue
		}
		rootPkgs[i] = rootPkg
		manifest = append(manifest, lines...)
	}
	if !allValid {
		t.FailNow()
	}
	mfPath := filepath.Join(tmp, "manifest")
	writeLines(t, mfPath, manifest)

	args := []string{
		"-std", stdDir(),
		"-steps", strconv.FormatInt(budgetSteps, 10),
		"-depth", strconv.Itoa(budgetDepth),
		"-cases", "100",
		"-entry", "EvalMain",
		"-stats",
		"-corpus", mfPath,
	}
	if each {
		args = append([]string{"-each"}, args...)
	}
	dumpLines := runDump(t, ctx, bin, args...)
	byPkg, statsByPkg := parseEvalDumpOutput(t, dumpLines)

	goBudget := eval.Budget{
		Steps: budgetSteps,
		Depth: budgetDepth,
		Bytes: 8 << 20,
		Seed:  1,
	}

	mismatches := 0
	for i, c := range cases {
		rootPkg := rootPkgs[i]
		rep, err := eval.Run(rootPkg, goBudget)
		if err != nil {
			t.Fatalf("case %s: Go eval.Run error: %v", c.Name, err)
		}
		gotOuts := byPkg[i]
		if !each && i > 0 {
			continue
		}

		var gotObligations []evalDumpOutcome
		var gotEntry *evalDumpOutcome
		for j := range gotOuts {
			o := gotOuts[j]
			if o.Kind == "expr" {
				cp := o
				gotEntry = &cp
				continue
			}
			if o.Kind == "error" {
				t.Errorf("case %s: compiler/eval compile error: %s", c.Name, o.Reason)
				mismatches++
				continue
			}
			gotObligations = append(gotObligations, o)
		}

		if len(gotObligations) != len(rep.Results) {
			t.Errorf("case %s: obligation count mismatch: compiler/eval=%d (%+v), go=%d (%+v)",
				c.Name, len(gotObligations), gotObligations, len(rep.Results), rep.Results)
			mismatches++
			continue
		}
		for k, gr := range rep.Results {
			eo := gotObligations[k]
			wantKind, wantName := normalizeGoResultName(gr.Name)
			if eo.Kind != wantKind || eo.Name != wantName {
				t.Errorf("case %s [%d]: identity mismatch: got (%s,%q), want (%s,%q)",
					c.Name, k, eo.Kind, eo.Name, wantKind, wantName)
				mismatches++
			}
			if eo.Status != string(gr.Status) {
				t.Errorf("case %s [%s]: status mismatch: compiler/eval=%q (reason=%q), go=%q (reason=%q)",
					c.Name, gr.Name, eo.Status, eo.Reason, gr.Status, gr.Reason)
				mismatches++
			}
		}

		if c.HasEvalMain {
			ex, err := eval.RunEntry(rootPkg, "EvalMain", goBudget, eval.Process{})
			if err != nil {
				t.Errorf("case %s: Go RunEntry(EvalMain) error: %v", c.Name, err)
				mismatches++
			} else if ex.Incomplete != "" || ex.Skipped != "" {
				t.Errorf("case %s: Go RunEntry(EvalMain) incomplete=%q skipped=%q", c.Name, ex.Incomplete, ex.Skipped)
				mismatches++
			} else if gotEntry == nil {
				t.Errorf("case %s: compiler/eval produced no EvalMain outcome", c.Name)
				mismatches++
			} else {
				wantVal := strings.TrimSuffix(ex.Stdout, "\n")
				if gotEntry.Status != "passed" {
					t.Errorf("case %s: compiler/eval EvalMain status=%q reason=%q", c.Name, gotEntry.Status, gotEntry.Reason)
					mismatches++
				} else if gotEntry.Value != wantVal {
					t.Errorf("case %s: EvalMain value mismatch:\n  compiler/eval: %q\n  go eval:       %q",
						c.Name, gotEntry.Value, wantVal)
					mismatches++
				}
			}
		}
		st := statsByPkg[i]
		if st.TotalSteps == 0 && len(gotObligations) > 0 && gotObligations[0].Status != "not run" {
			t.Logf("case %s: warning total_steps=0", c.Name)
		}
	}
	if mismatches > 0 {
		t.Fatalf("parity suite failed with %d mismatches", mismatches)
	}
}

func TestCompilerEvalAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	corpus := evalParityCorpus()
	preTmp := t.TempDir()
	allValid := true
	for i, c := range corpus {
		if _, _, ok := checkEvalCaseUnits(t, c, filepath.Join(preTmp, fmt.Sprintf("pre_%d", i))); !ok {
			allValid = false
		}
	}
	if !allValid {
		t.FailNow()
	}
	bin := evalDumpBin(t, ctx)
	var normalCases []evalCase
	var lowStepCases []evalCase
	var lowDepthCases []evalCase
	var multiCases []evalCase
	for _, c := range corpus {
		switch {
		case c.Name == "budget_steps_exhausted":
			lowStepCases = append(lowStepCases, c)
		case c.Name == "budget_depth_exhausted":
			lowDepthCases = append(lowDepthCases, c)
		case len(c.Pkgs) > 1:
			multiCases = append(multiCases, c)
		default:
			normalCases = append(normalCases, c)
		}
	}

	t.Run("normal_corpus", func(t *testing.T) {
		runEvalCaseParity(t, ctx, bin, normalCases, true, 1_000_000, 256)
	})
	t.Run("step_budget_exhaustion", func(t *testing.T) {
		runEvalCaseParity(t, ctx, bin, lowStepCases, true, 500, 256)
	})
	t.Run("depth_budget_exhaustion", func(t *testing.T) {
		runEvalCaseParity(t, ctx, bin, lowDepthCases, true, 1_000_000, 16)
	})
	t.Run("multi_pkg", func(t *testing.T) {
		for _, mc := range multiCases {
			runEvalCaseParity(t, ctx, bin, []evalCase{mc}, false, 1_000_000, 256)
		}
	})
}

func TestCompilerEvalStepScalingAndCount(t *testing.T) {
	needsBuild(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bin := evalDumpBin(t, ctx)

	makeLoopCase := func(name string, n int) evalCase {
		src := fmt.Sprintf(`
func work(n int) int {
	let mut acc = 0
	for i in 0..n {
		acc = (acc + i*3 + 1) %% 1000003
	}
	acc
}

test WorkLinear {
	assert work(%d) >= 0
}
`, n)
		return singlePkgCase(name, src, false)
	}

	tmp := t.TempDir()
	cases := []evalCase{makeLoopCase("scale_200", 200), makeLoopCase("scale_400", 400)}
	var manifest []string
	for i, c := range cases {
		cDir := filepath.Join(tmp, fmt.Sprintf("s%d", i))
		lines, _, ok := checkEvalCaseUnits(t, c, cDir)
		if !ok {
			t.FailNow()
		}
		manifest = append(manifest, lines...)
	}
	mfPath := filepath.Join(tmp, "manifest")
	writeLines(t, mfPath, manifest)

	dumpLines := runDump(t, ctx, bin, "-each", "-stats", "-std", stdDir(), "-corpus", mfPath)
	byPkg, stats := parseEvalDumpOutput(t, dumpLines)
	if len(byPkg[0]) != 1 || byPkg[0][0].Status != "passed" {
		t.Fatalf("scale_200 unexpected output: %+v", byPkg[0])
	}
	if len(byPkg[1]) != 1 || byPkg[1][0].Status != "passed" {
		t.Fatalf("scale_400 unexpected output: %+v", byPkg[1])
	}
	s200 := stats[0].TotalSteps
	s400 := stats[1].TotalSteps
	if s200 == 0 || s400 <= s200 {
		t.Fatalf("expected non-zero increasing steps: s200=%d s400=%d", s200, s400)
	}
	ratio := float64(s400) / float64(s200)
	if math.Abs(ratio-2.0) > 0.35 {
		t.Fatalf("expected ~2x step scaling for 2x loop iterations: s200=%d s400=%d ratio=%.3f", s200, s400, ratio)
	}
	t.Logf("deterministic steps: s200=%d s400=%d ratio=%.3f", s200, s400, ratio)

	if err := opcount.Available(); err == nil {
		cmd := exec.CommandContext(ctx, bin, "-each", "-stats", "-std", stdDir(), "-corpus", mfPath)
		run, cerr := opcount.Count(cmd, 0)
		if cerr == nil {
			t.Logf("opcount user instructions for evaldump scaling corpus: %d", run.Instructions)
		} else {
			t.Logf("opcount measurement skipped: %v", cerr)
		}
	} else {
		t.Logf("opcount PMU unavailable on host: %v", err)
	}
}

func BenchmarkCompilerEvalVM(b *testing.B) {
	if testing.Short() {
		b.Skip("skipping compiler eval benchmark in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := repoRoot()
	cli := filepath.Join(b.TempDir(), "ergo")
	buildErgo := exec.CommandContext(ctx, "go", "build", "-o", cli, "./cmd/ergo")
	buildErgo.Dir = root
	if out, err := buildErgo.CombinedOutput(); err != nil {
		b.Fatalf("build ergo: %v\n%s", err, out)
	}
	bin := filepath.Join(b.TempDir(), "evaldump")
	buildDump := exec.CommandContext(ctx, cli, "build", "-o", bin, "./cmd/evaldump")
	buildDump.Dir = filepath.Join(root, "compiler")
	if out, err := buildDump.CombinedOutput(); err != nil {
		b.Fatalf("build evaldump: %v\n%s", err, out)
	}
	tmp := b.TempDir()
	src := `
func fib(n int) int {
	if n <= 1 { n } else { fib(n-1) + fib(n-2) }
}

test FibBench {
	assert fib(15) == 610
}
`
	fp := filepath.Join(tmp, "main.ergo")
	if err := os.WriteFile(fp, []byte(src), 0o644); err != nil {
		b.Fatal(err)
	}
	ef := filepath.Join(tmp, "ext")
	ff := filepath.Join(tmp, "foreign")
	_ = os.WriteFile(ef, nil, 0o644)
	_ = os.WriteFile(ff, nil, 0o644)
	mf := filepath.Join(tmp, "manifest")
	_ = os.WriteFile(mf, []byte(fmt.Sprintf("bench/fib %s %s %s\n", ef, ff, fp)), 0o644)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cmd := exec.CommandContext(ctx, bin, "-each", "-std", stdDir(), "-corpus", mf)
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("evaldump: %v\n%s", err, out)
		}
	}
}
