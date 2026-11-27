package stdlib_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

const regexNativeTestSrc = `import regex

type Date derive regex.Pattern("^(?<Year>[0-9]{4})-(?<Month>[0-9]{2})(-(?<Day>[0-9]{2}))?$")
type Token derive regex.Pattern("(?<Num>[0-9]+)|(?<Word>[a-z]+)")
type LongCode derive regex.Pattern("^(?:(?i:tk))-(?<Prefix>[a-z]{26})-(?<Num>[0-9]{4})$")
type BitWindow derive regex.Pattern("[01]*0[01]{5}")
type AmbTag derive regex.Pattern("(?<Head>[ab]+)b")
type LazyPair derive regex.Pattern("(?<Lead>a+?)(?<Tail>a+)")
type NestedRec derive regex.Pattern("(?<Outer>(?<Inner>[0-9]{2})?)-(?<Tag>[a-z]+)")
type PriorityA derive regex.Pattern("a|ab")
type PriorityB derive regex.Pattern("ab|a")
type LazyPlus derive regex.Pattern("a+?")
type LazyBounded derive regex.Pattern("(?<Pre>a{1,3}?)(?<Rest>a+)")
type FoldSwitch derive regex.Pattern("(?i)ab(?-i)CD")
type UniClass derive regex.Pattern("[\\u00e9-\\u00ea\\u{1F600}\\x{2603}]+")
type DollarOnly derive regex.Pattern("a+$")
type NestedPairs derive regex.Pattern("(?<Item>(?<K>[a-z]+)=(?<V>[0-9]+);)*")

test DirectTableAndNFABasicsNative {
	let d = try ParseDate("2026-11-27")
	assert d.Year == "2026" && d.Month == "11"
	match d.Day {
	case Some as day:
		assert day.Value == "27"
	case None:
		assert false
	}
	let dFail = ParseDate("2026-1x-27") catch {
	case NoMatch as m:
		assert m.At == 6
		Date{Year: "", Month: "", Day: None}
	}
	assert dFail.Year == ""

	match try ParseToken("9001") {
	case Num as n:
		assert n.Value == "9001"
	case Word:
		assert false
	}

	let lc = try ParseLongCode("tK-abcdefghijklmnopqrstuvwxyz-1234")
	assert lc.Prefix == "abcdefghijklmnopqrstuvwxyz" && lc.Num == "1234"
	let _ = try ParseBitWindow("111010101")
	let lp = try ParseLazyPair("aaaa")
	assert lp.Lead == "a" && lp.Tail == "aaa"
}

test SemanticsPriorityLazyAnchorsUTF8Native {
	let n1 = try ParseNestedRec("-ok")
	assert n1.Outer.Inner == None && n1.Tag == "ok"
	let n2 = try ParseNestedRec("42-ok")
	match n2.Outer.Inner {
	case Some as v:
		assert v.Value == "42"
	case None:
		assert false
	}

	let pa = FindPriorityA("xxab", 0)
	match pa {
	case Some as m:
		let (_, s, e) = m.Value
		assert s == 2 && e == 3
	case None:
		assert false
	}
	let pb = FindPriorityB("xxab", 0)
	match pb {
	case Some as m:
		let (_, s, e) = m.Value
		assert s == 2 && e == 4
	case None:
		assert false
	}

	let lp = FindLazyPlus("xxaaa", 0)
	match lp {
	case Some as m:
		let (_, s, e) = m.Value
		assert s == 2 && e == 3
	case None:
		assert false
	}

	let lb = try ParseLazyBounded("aaaaa")
	assert lb.Pre == "a" && lb.Rest == "aaaa"

	let _ = try ParseFoldSwitch("aBCD")
	let badFold = ParseFoldSwitch("abcd") catch {
	case NoMatch as m:
		assert m.At == 2
		FoldSwitch
	}
	let _ = badFold

	let _ = try ParseUniClass("é😀☃ê")
	match FindUniClass("xx😀êyy", 0) {
	case Some as m:
		let (_, s, e) = m.Value
		assert s == 2 && e == 8
	case None:
		assert false
	}

	match FindDollarOnly("xaa", 0) {
	case Some as m:
		let (_, s, e) = m.Value
		assert s == 1 && e == 3
	case None:
		assert false
	}
	assert FindDollarOnly("xaax", 0) == None

	let amb = ParseAmbTag("abba") catch {
	case NoMatch as m:
		assert m.At == 4
		AmbTag{Head: ""}
	}
	assert amb.Head == ""
	assert (try ParseAmbTag("ababb")).Head == "abab"

	let np = try ParseNestedPairs("a=1;bc=23;")
	assert np.Item.Len() == 2 && np.Item[0].K == "a" && np.Item[0].V == "1" && np.Item[1].K == "bc" && np.Item[1].V == "23"
}
`

func buildTestErgoBinary(t *testing.T) string {
	t.Helper()
	root := moduleRootDir(t)
	tmp := t.TempDir()
	ergoBin := filepath.Join(tmp, "bin", "ergo")
	if err := os.MkdirAll(filepath.Dir(ergoBin), 0o755); err != nil {
		t.Fatal(err)
	}
	build := ergobuild.Command(ergoBin, "./cmd/ergo", "-buildvcs=false")
	build.Dir = root
	if out, err := runBound(build); err != nil {
		t.Fatalf("build ergo: %v\n%s", err, out)
	}
	return ergoBin
}

const regexAllocMainSrc = `import go testing
import regex

type Date derive regex.Pattern("^(?<Year>[0-9]{4})-(?<Month>[0-9]{2})(-(?<Day>[0-9]{2}))?$")
type Token derive regex.Pattern("(?<Num>[0-9]+)|(?<Word>[a-z]+)")
type LongCode derive regex.Pattern("^(?:(?i:tk))-(?<Prefix>[a-z]{26})-(?<Num>[0-9]{4})$")
type BitWindow derive regex.Pattern("[01]*0[01]{5}")
type NestedRec derive regex.Pattern("(?<Outer>(?<Inner>[0-9]{2})?)-(?<Tag>[a-z]+)")

func allocs(f own func()) int needs go.Foreign {
	f()
	int(testing.AllocsPerRun(50, f))
}

func checkDateMatch() int {
	let d = ParseDate("2026-11-27") catch {
	case NoMatch:
		return 0
	}
	if d.Year == "2026" { 1 } else { 0 }
}

func checkDateFail() int {
	let _ = ParseDate("2026-1x-27") catch {
	case NoMatch as m:
		return m.At
	}
	-1
}

func checkToken() int {
	match (ParseToken("9001") catch {
	case NoMatch:
		return 0
	}) {
	case Num as n:
		n.Value.Len()
	case Word:
		0
	}
}

func checkLongCodeMatch() int {
	let lc = ParseLongCode("tK-abcdefghijklmnopqrstuvwxyz-1234") catch {
	case NoMatch:
		return 0
	}
	lc.Num.Len()
}

func checkLongCodeFail() int {
	let _ = ParseLongCode("tk-abcdefghijklmnopqrstuvwxyz-12x4") catch {
	case NoMatch as m:
		return m.At
	}
	-1
}

func checkBitWindow() int {
	let _ = ParseBitWindow("111010101") catch {
	case NoMatch:
		return 0
	}
	1
}

func checkNested() int {
	let n = ParseNestedRec("42-ok") catch {
	case NoMatch:
		return 0
	}
	if n.Tag == "ok" { 1 } else { 0 }
}

func main() needs Console | go.Foreign {
	let a0 = allocs(func() {})
	let a1 = allocs(func() { let _ = checkDateMatch() })
	let a2 = allocs(func() { let _ = checkDateFail() })
	let a3 = allocs(func() { let _ = checkToken() })
	let a4 = allocs(func() { let _ = checkLongCodeMatch() })
	let a5 = allocs(func() { let _ = checkLongCodeFail() })
	let a6 = allocs(func() { let _ = checkBitWindow() })
	let a7 = allocs(func() { let _ = checkNested() })
	Console.Println("allocs:\(a0):\(a1):\(a2):\(a3):\(a4):\(a5):\(a6):\(a7)")
}
`

// TestStdRegexNativeEndToEndAndZeroAlloc verifies end-to-end native compilation
// with ERGO_FALLBACK=off across Direct DFA, Table DFA (>24 states), NFA fallback,
// zero-alloc fixed/optional/nested/union/table ParseT via testing.AllocsPerRun, and semantics.
func TestStdRegexNativeEndToEndAndZeroAlloc(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds ergo binary")
	}
	ergoBin := buildTestErgoBinary(t)
	modDir := filepath.Join(t.TempDir(), "regexnative")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module regexnative\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "regex_native_test.ergo"), []byte(regexNativeTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ergoBin, "test", "--native", "--count", "1", ".")
	cmd.Dir = modDir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	out, err := runBound(cmd)
	if err != nil {
		t.Fatalf("ergo test --native failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ok DirectTableAndNFABasicsNative") || !strings.Contains(out, "ok SemanticsPriorityLazyAnchorsUTF8Native") {
		t.Fatalf("expected native test passes in output:\n%s", out)
	}

	allocDir := filepath.Join(t.TempDir(), "regexalloc")
	if err := os.MkdirAll(allocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allocDir, "ergo.mod"), []byte("module regexalloc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allocDir, "main.ergo"), []byte(regexAllocMainSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd := exec.Command(ergoBin, "run", ".")
	runCmd.Dir = allocDir
	runCmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	allocOut, err := runBound(runCmd)
	if err != nil {
		t.Fatalf("ergo run zero-alloc check failed: %v\n%s", err, allocOut)
	}
	if strings.TrimSpace(allocOut) != "allocs:0:0:0:0:0:0:0:0" {
		t.Fatalf("expected zero allocations on all DFA paths, got %q", strings.TrimSpace(allocOut))
	}
}

func lookupMapPattern(t *testing.T) (*check.Package, *check.Func) {
	t.Helper()
	pkg, err := check.Std.Import("regex")
	if err != nil {
		t.Fatalf("import regex: %v", err)
	}
	for _, fn := range pkg.Funcs {
		if fn.Name == "MapPattern" {
			return pkg, fn
		}
	}
	t.Fatalf("std/regex missing MapPattern")
	return nil, nil
}

func quoteErgo(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, `\x%02x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// TestStdRegexDifferentialWithGoRegexp compiles a diverse suite of patterns
// through std/regex end-to-end with ERGO_FALLBACK=off, confirms expected Direct DFA,
// Table DFA, and NFA engine routing in generated source, and differentially verifies
// Parse and Find results across witness and UTF-8 inputs against Go's regexp.Regexp.
func TestStdRegexDifferentialWithGoRegexp(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds ergo binary")
	}
	type patCase struct {
		name string
		pat  string
		mode string // "direct", "table", "nfa"
	}
	cases := []patCase{
		{"P0Dot", "^a.c$", "direct"},
		{"P1Word", `^\w+$`, "direct"},
		{"P2NonDigit", `^\D+$`, "table"},
		{"P3Space", `^\s+$`, "direct"},
		{"P4FoldGroup", `(?i:ab)[c-e](?i)x(?-i)Y`, "direct"},
		{"P5NegFold", `(?i)[^a]+`, "table"},
		{"P6LazyPlus", `a+?b`, "direct"},
		{"P7LazyStar", `a*?b`, "direct"},
		{"P8LazyQuest", `a??a`, "direct"},
		{"P9LazyBrace", `a{1,3}?b`, "direct"},
		{"P10Alt", `a|ab`, "direct"},
		{"P11NonCap", `(?:ab|cd)+`, "direct"},
		{"P12EdgeClass", `[-a\]]+`, "direct"},
		{"P13Uni", `[\u00e9-\u00ea\u{1F600}]+`, "direct"},
		{"P14Table", `[a-z]{26}-[0-9]{2}`, "table"},
		{"P15BitTable", `[01]*0[01]{5}`, "table"},
		{"P16NFAAmb", `(?<H>[ab]+)b`, "nfa"},
		{"P17BranchAnchor", `^foo|bar$`, "direct"},
	}

	inputs := []string{
		"",
		"xxfoo",
		"foo",
		"xbar",
		"bar",
		"a",
		"ab",
		"aba",
		"abab",
		"aab",
		"aaab",
		"aaaab",
		"abc",
		"axc",
		"a\nc",
		"aéc",
		"a😀c",
		"AbCXy",
		"aBcDy",
		"AbCxy",
		"xxab",
		"xaaab",
		"xxa",
		"A",
		"bA",
		"éê😀",
		"01_AZ",
		"é!",
		" \t\n\r\f",
		"-a]-",
		"abcdefghijklmnopqrstuvwxyz-42",
		"abcdefghijklmnopqrstuvwxyz-4",
		"111010101",
		"111010",
		"ccababbxx",
	}

	pkg, fn := lookupMapPattern(t)

	for _, tc := range cases {
		out, evalErr := eval.InvokeMapper(pkg, fn, eval.MapperInput{
			Target: tc.name,
			Origin: eval.MapperOrigin{
				Kind:    eval.OriginInline,
				Package: "diffcheck",
				File:    "main.ergo",
				Target:  tc.name,
				Line:    1,
				Col:     1,
			},
			MediaKind: "regex",
			Bytes:     []byte(tc.pat),
		}, eval.DefaultMapperBudget())
		if evalErr != nil {
			t.Fatalf("%s MapPattern eval err: %s", tc.name, evalErr.Message)
		}
		if len(out.Diags) > 0 {
			t.Fatalf("%s MapPattern diags: %+v", tc.name, out.Diags)
		}
		pStart := strings.Index(out.Source, "func Parse"+tc.name+"(")
		fStart := strings.Index(out.Source, "func Find"+tc.name+"(")
		if pStart < 0 || fStart <= pStart {
			t.Fatalf("%s missing Parse/Find in generated source", tc.name)
		}
		parseBody := out.Source[pStart:fStart]
		hasTable := strings.Contains(parseBody, "let classMap = []uint8{")
		hasExec := strings.Contains(parseBody, "_rxExec_"+tc.name+"(s, true, 0)")
		switch tc.mode {
		case "direct":
			if hasTable || hasExec {
				t.Fatalf("%s expected direct DFA, got hasTable=%v hasExec=%v", tc.name, hasTable, hasExec)
			}
		case "table":
			if !hasTable || hasExec {
				t.Fatalf("%s expected table DFA, got hasTable=%v hasExec=%v", tc.name, hasTable, hasExec)
			}
		case "nfa":
			if !hasExec {
				t.Fatalf("%s expected NFA fallback (_rxExec_), got:\n%s", tc.name, parseBody)
			}
		}
	}

	var prog strings.Builder
	for _, tc := range cases {
		fmt.Fprintf(&prog, "type %s derive regex.Pattern(%q)\n", tc.name, tc.pat)
		fmt.Fprintf(&prog, "func run%s(s string) string {\n", tc.name)
		fmt.Fprintf(&prog, "\tlet mut p = 1\n")
		fmt.Fprintf(&prog, "\tlet _ = Parse%s(s) catch {\n", tc.name)
		fmt.Fprintf(&prog, "\tcase NoMatch:\n")
		fmt.Fprintf(&prog, "\t\tp = 0\n")
		fmt.Fprintf(&prog, "\t\treturn \"%s|0|\" + match Find%s(s, 0) {\n", tc.name, tc.name)
		fmt.Fprintf(&prog, "\t\tcase Some as m:\n")
		fmt.Fprintf(&prog, "\t\t\tlet (_, st, en) = m.Value\n")
		fmt.Fprintf(&prog, "\t\t\t\"1:\\(st):\\(en)\"\n")
		fmt.Fprintf(&prog, "\t\tcase None:\n")
		fmt.Fprintf(&prog, "\t\t\t\"0:-1:-1\"\n")
		fmt.Fprintf(&prog, "\t\t}\n")
		fmt.Fprintf(&prog, "\t}\n")
		fmt.Fprintf(&prog, "\t\"%s|\\(p)|\" + match Find%s(s, 0) {\n", tc.name, tc.name)
		fmt.Fprintf(&prog, "\tcase Some as m:\n")
		fmt.Fprintf(&prog, "\t\tlet (_, st, en) = m.Value\n")
		fmt.Fprintf(&prog, "\t\t\"1:\\(st):\\(en)\"\n")
		fmt.Fprintf(&prog, "\tcase None:\n")
		fmt.Fprintf(&prog, "\t\t\"0:-1:-1\"\n")
		fmt.Fprintf(&prog, "\t}\n")
		fmt.Fprintf(&prog, "}\n\n")
	}
	prog.WriteString("func main() needs Console {\n")
	for _, tc := range cases {
		for _, in := range inputs {
			fmt.Fprintf(&prog, "\tConsole.Println(run%s(%s))\n", tc.name, quoteErgo(in))
		}
	}
	prog.WriteString("}\n")

	ergoBin := buildTestErgoBinary(t)
	modDir := filepath.Join(t.TempDir(), "regexdiff")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "ergo.mod"), []byte("module regexdiff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "main.ergo"), []byte("import regex\n\n"+prog.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(ergoBin, "run", ".")
	cmd.Dir = modDir
	cmd.Env = append(os.Environ(), "ERGO_FALLBACK=off")
	runOut, err := runBound(cmd)
	if err != nil {
		t.Fatalf("ergo run differential suite failed: %v\n%s", err, runOut)
	}

	lines := strings.Split(strings.TrimSpace(runOut), "\n")
	wantCount := len(cases) * len(inputs)
	if len(lines) != wantCount {
		t.Fatalf("got %d output lines, want %d\nout:\n%s", len(lines), wantCount, runOut)
	}

	idx := 0
	for _, tc := range cases {
		goPat := strings.ReplaceAll(tc.pat, "(?<H>", "(?P<H>")
		goPat = strings.ReplaceAll(goPat, `\u00e9`, `\x{00e9}`)
		goPat = strings.ReplaceAll(goPat, `\u00ea`, `\x{00ea}`)
		goPat = strings.ReplaceAll(goPat, `\u{1F600}`, `\x{1F600}`)
		reFull := regexp.MustCompile(`\A(?:` + goPat + `)\z`)
		reFind := regexp.MustCompile(goPat)
		for _, in := range inputs {
			gotLine := strings.TrimSpace(lines[idx])
			idx++
			wantParse := 0
			if reFull.MatchString(in) {
				wantParse = 1
			}
			loc := reFind.FindStringIndex(in)
			wantFind := "0:-1:-1"
			if loc != nil {
				wantFind = fmt.Sprintf("1:%d:%d", loc[0], loc[1])
			}
			wantLine := fmt.Sprintf("%s|%d|%s", tc.name, wantParse, wantFind)
			if gotLine != wantLine {
				t.Fatalf("case %s pat=%q input=%q: got %q, want %q (Go regexp)", tc.name, tc.pat, in, gotLine, wantLine)
			}
		}
	}
}

// TestStdRegexDiagnosticsAndBudgets validates exact byte spans and diagnostic codes
// for unsupported syntax, malformed ranges/escapes, empty-body repeats, and NFA budgets.
func TestStdRegexDiagnosticsAndBudgets(t *testing.T) {
	pkg, fn := lookupMapPattern(t)

	for _, tc := range []struct {
		name     string
		pat      string
		wantCode string
		wantStart uint32
		wantEnd   uint32
		wantSub  string
	}{
		{"mid caret", "a^b", "R001", 1, 2, "drop ^"},
		{"mid dollar", "a$b", "R001", 1, 2, "drop $"},
		{"leading dollar", "$a", "R001", 0, 1, "drop $"},
		{"lookahead pos", "(?=a)", "R001", 0, 3, "lookahead"},
		{"lookahead neg", "(?!a)", "R001", 0, 3, "lookahead"},
		{"lookbehind pos", "(?<=a)", "R001", 0, 4, "lookbehind"},
		{"lookbehind neg", "(?<!a)", "R001", 0, 4, "lookbehind"},
		{"py group", "(?P<X>a)", "R001", 0, 4, "(?<Name>"},
		{"backref digit", `a\1`, "R001", 1, 3, "backreferences"},
		{"backref named", `\k<X>`, "R001", 0, 2, "backreferences"},
		{"nullable repeat", "(a?)*", "R001", 0, 5, "empty string"},
		{"nullable capture repeat", "(?<X>a|b?)+", "R001", 0, 11, "empty string"},
		{"possessive", "a++", "R001", 1, 3, "possessive"},
		{"posix class", "[[:alpha:]]", "R001", 1, 10, "POSIX"},
		{"bad range", "[z-a]", "R001", 1, 4, "end < start"},
		{"surrogate", `\uD800`, "R001", 0, 6, "surrogate"},
		{"bad brace", "a{5,2}", "R001", 1, 6, "max < min"},
		{"unsupported flag", "(?m)a", "R001", 0, 3, "flag"},
		{"nfa cost cap", "[a-z]{1000}{100}", "R005", 0, 16, "instruction limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, evalErr := eval.InvokeMapper(pkg, fn, eval.MapperInput{
				Target: "T",
				Origin: eval.MapperOrigin{
					Kind:    eval.OriginInline,
					Package: "diagtest",
					File:    "main.ergo",
					Target:  "T",
					Line:    1,
					Col:     1,
				},
				MediaKind: "regex",
				Bytes:     []byte(tc.pat),
			}, eval.DefaultMapperBudget())
			if evalErr != nil {
				t.Fatalf("eval err: %s", evalErr.Message)
			}
			if len(out.Diags) != 1 {
				t.Fatalf("got %d diags (%+v), want 1", len(out.Diags), out.Diags)
			}
			d := out.Diags[0]
			if d.Code != tc.wantCode || d.Span.Start != tc.wantStart || d.Span.End != tc.wantEnd || !strings.Contains(d.Message, tc.wantSub) {
				t.Fatalf("got diag {Code:%q Span:%d..%d Msg:%q}, want {Code:%q Span:%d..%d Sub:%q}",
					d.Code, d.Span.Start, d.Span.End, d.Message, tc.wantCode, tc.wantStart, tc.wantEnd, tc.wantSub)
			}
		})
	}
}
