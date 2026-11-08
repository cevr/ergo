package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestStdTextRuns2 (std-align S1c; stdlib.md, strings and strconv): each
// function S1c adds calls its runtime/v1 body or its Ergo body and prints
// one line; one catch matches Malformed and OutOfRange of strconv by a
// match, with no string test. Its test build restates the laws whose ledger
// runner is this test (the ones over an Ergo body: Cut and Lines) and runs
// them with --seed 1.
func TestStdTextRuns2(t *testing.T) {
	const program = `import strings
import strconv
import utf8

func parse(s string) string {
	let n = strconv.ParseInt(s, 0, 64) catch {
	case strconv.Malformed as m:
		return "malformed \(m.Text)"
	case strconv.OutOfRange as o:
		return "too big \(o.Text)"
	}
	"int \(n)"
}

func float(s string) string {
	let f = strconv.ParseFloat(s) catch {
	case strconv.Malformed:
		return "malformed"
	case strconv.OutOfRange:
		return "too big"
	}
	strconv.FormatFloat(f, 'g', -1)
}

func boolean(s string) string {
	let b = strconv.ParseBool(s) catch {
	case strconv.Malformed:
		return "malformed"
	}
	strconv.FormatBool(b)
}

func unquote(s string) string {
	let u = strconv.Unquote(s) catch {
	case strconv.Malformed:
		return "malformed"
	}
	"[\(u)]"
}

func atoi(s string) string {
	let n = strconv.Atoi(s) catch {
	case strconv.Malformed:
		return "malformed"
	case strconv.OutOfRange:
		return "out of range"
	}
	"\(n)"
}

func main() needs Console {
	Console.Println(strings.ReplaceAll("a-b-c", "-", "+"))
	Console.Println("\(strings.LastIndex("go gopher", "go")) \(strings.LastIndex("go", "x"))")
	Console.Println("\(strings.Count("cheese", "e")) \(strings.Count("five", ""))")
	Console.Println(strings.Join(strings.SplitN("a,b,c,d", ",", 2), "|"))
	Console.Println("\(len(strings.SplitN("a,b", ",", 0)))")
	Console.Println("\(strings.EqualFold("Go", "GO")) \(strings.EqualFold("Go", "Gx"))")
	Console.Println("\(strings.ContainsRune("abc", 'b')) \(strings.ContainsRune("abc", 'é'))")
	for line in strings.Lines("one\ntwo\nthree") {
		Console.Println("<\(strings.TrimSuffix(line, "\n"))>\(strings.HasSuffix(line, "\n"))")
	}
	Console.Println("\(utf8.RuneCount("héllo")) \(utf8.RuneCount("\xff"))")
	Console.Println(parse("0x10"))
	Console.Println(parse("1_000"))
	Console.Println(parse("x"))
	Console.Println(parse("99999999999999999999"))
	Console.Println(float("2.5"))
	Console.Println(float("1e400"))
	Console.Println(float("x"))
	Console.Println(boolean("T"))
	Console.Println(boolean("maybe"))
	Console.Println(unquote("\"a\\tb\""))
	Console.Println(unquote("\"a"))
	Console.Println(atoi("12"))
	Console.Println(atoi("x"))
	Console.Println(atoi("99999999999999999999"))
	Console.Println(strconv.FormatFloat(1.5, 'e', 2))
	// The upstream cases of conformance/effect-cases.json at 460272d3,
	// each after a line "== <case id>": String.test.ts:454 replaceAll,
	// :554 linesWithSeparators, and Number.test.ts:430 parse (Ergo
	// differs: no trimming, a typed failure).
	Console.Println("== effect.string.replace-all")
	Console.Println(strings.ReplaceAll("ababb", "b", "c"))
	Console.Println("== effect.string.lines-with-separators")
	for text in []string{"hello\nworld\n", "a\r\nb\r\n", "hello", ""} {
		let mut parts = []string{}
		for line in strings.Lines(text) {
			parts = parts.Append(strconv.Quote(line))
		}
		Console.Println("[\(strings.Join(parts, " "))]")
	}
	Console.Println("== effect.number.parse")
	for text in []string{"42", "3.14", " 42 ", "NaN", "Infinity", "-Infinity", "", "   ", "not a number"} {
		Console.Println(float(text))
	}
}
`
	const want = "a+b+c\n3 -1\n3 5\na|b,c,d\n0\ntrue false\ntrue false\n<one>true\n<two>true\n<three>false\n5 1\nint 16\nint 1000\nmalformed x\ntoo big 99999999999999999999\n2.5\ntoo big\nmalformed\ntrue\nmalformed\n[a\tb]\nmalformed\n12\nmalformed\nout of range\n1.50e+00\n"

	const laws = `import strings

test StringsCutRebuilds(s string, sep string) {
	match strings.Cut(s, sep) {
	case Some[strings.Halves] as h:
		assert h.Value.Before+sep+h.Value.After == s && strings.Index(h.Value.Before+sep, sep) == len(h.Value.Before)
	case None:
		assert !strings.Contains(s, sep)
	}
	let i = len(s) / 2
	let inner = strings.Slice(s, 0, i) + sep + strings.Slice(s, i, len(s))
	match strings.Cut(inner, sep) {
	case Some[strings.Halves] as h:
		assert h.Value.Before+sep+h.Value.After == inner && strings.Index(h.Value.Before+sep, sep) == len(h.Value.Before)
	case None:
		assert false
	}
}

test StringsLinesRebuild(s string, t string) {
	for text in []string{s, s + "\n" + t, "\n" + s + "\n\n" + t + "\n", "", "\n", "a\r\nb\xff"} {
		let mut joined = ""
		let mut n = 0
		for line in strings.Lines(text) {
			joined = joined + line
			n = n + 1
			assert line != ""
			assert strings.Count(line, "\n") == 0 || strings.HasSuffix(line, "\n") && strings.Count(line, "\n") == 1
		}
		assert joined == text
		assert n == strings.Count(text, "\n")+last(text)
	}
}

test StringsLinesStops(s string, t string) {
	for text in []string{s, s + "\n" + t, "\n" + s + "\n\n" + t + "\n", "", "\n", "a\r\nb\xff"} {
		let lines = strings.Lines(text)
		let n = strings.Count(text, "\n") + last(text)
		for k in 1..n+2 {
			let mut calls = 0
			lines(func(line string) bool {
				calls = calls + 1
				calls < k
			})
			assert calls == k || k > n && calls == n
		}
	}
}

// last is 1 when text ends in a line with no line feed.
func last(text string) int {
	if strings.HasSuffix(text, "\n") || text == "" {
		return 0
	}
	1
}
`
	run := mustLower(t, "text2", program)
	exes := buildAll(t, []*lowered{run, lowerTestBuild(t, "text2laws", laws)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["text2"])
	head, sections, _ := strings.Cut(stdout, "== ")
	if code != 0 || head != want {
		t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %s", code, head, want, firstLines(stderr, 10))
	}
	// The conformance cases, each a subtest named after its case id in
	// conformance/effect-cases.json (scripts/conformance: a row's evidence
	// test runs the subtest of its case).
	t.Run("effect.string.replace-all", func(t *testing.T) {
		caseOutput(t, sections, "acacc\n")
	})
	t.Run("effect.string.lines-with-separators", func(t *testing.T) {
		caseOutput(t, sections, `["hello\n" "world\n"]`+"\n"+`["a\r\n" "b\r\n"]`+"\n"+`["hello"]`+"\n[]\n")
	})
	t.Run("effect.number.parse", func(t *testing.T) {
		caseOutput(t, sections, "42\n3.14\nmalformed\nNaN\n+Inf\n-Inf\nmalformed\nmalformed\nmalformed\n")
	})
	stdout, stderr, code = runExeArgs(t, exes["text2laws"], "--seed", "1")
	if code != 0 {
		t.Errorf("the laws: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 10))
	}
	lawledger.Run(t, lawsPassed(t, map[string]string{
		"strings CutRebuilds":  `CutRebuilds(s string, sep string) { match Cut(s, sep) { case Some[Halves] as h: h.Value.Before+sep+h.Value.After == s && Index(h.Value.Before+sep, sep) == len(h.Value.Before) case None: !Contains(s, sep) } }`,
		"strings LinesRebuild": `LinesRebuild(s string) { let mut joined = "" for line in Lines(s) { joined = joined + line } joined == s }`,
		"strings LinesStops":   `LinesStops(s string) { let lines = Lines(s) let mut n = 0 lines(func(line string) bool { n = n + 1 true }) for k in 1..n+2 { let mut calls = 0 lines(func(line string) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	}, func(key string) string {
		return "Strings" + strings.TrimPrefix(key, "strings ")
	}, laws, stdout))
}

// caseOutput checks the output section of the subtest's conformance case:
// in sections, the lines after "== <case id>\n" up to the next "== ".
func caseOutput(t *testing.T, sections, want string) {
	t.Helper()
	id := t.Name()[strings.LastIndexByte(t.Name(), '/')+1:]
	for section := range strings.SplitSeq("== "+sections, "== ") {
		if name, got, ok := strings.Cut(section, "\n"); ok && name == id {
			if got != want {
				t.Errorf("case %s: output %q, want %q", id, got, want)
			}
			return
		}
	}
	t.Errorf("case %s: no output section in %q", id, sections)
}

// BenchmarkTextErgoBodies (std-align S1c): the Ergo bodies strings.Cut and
// strings.Lines, emitted and measured in process, against Go's
// strings.Cut and strings.Lines on the same inputs. Cut is expected one
// allocation more than Go until X1 (its Option[Halves] result).
func BenchmarkTextErgoBodies(b *testing.B) {
	const src = `import strings

func CutBefore(s string, sep string) int {
	match strings.Cut(s, sep) {
	case Some[strings.Halves] as h:
		len(h.Value.Before) + len(h.Value.After)
	case None:
		-1
	}
}

func LinesLength(s string) int {
	let mut n = 0
	for line in strings.Lines(s) {
		n = n + len(line)
	}
	n
}

func main() needs Console {
	Console.Println("\(CutBefore("a=b", "=")) \(LinesLength("a\nb"))")
}
`
	emittedBench(b, "textbodies", src, func(mainGo string) string {
		return `package main

import (
	"strings"
	"testing"
)

var cutText = strings.Repeat("alpha,beta,gamma ", 4) + "key=value"
var linesText = strings.Repeat("a line of text\n", 64) + "last"
var sink int

func BenchmarkCutErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "CutBefore", `cutText, "="`) + `
	}
}

func BenchmarkCutGo(b *testing.B) {
	for range b.N {
		before, after, found := strings.Cut(cutText, "=")
		if found {
			sink = len(before) + len(after)
		} else {
			sink = -1
		}
	}
}

func BenchmarkLinesErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "LinesLength", "linesText") + `
	}
}

func BenchmarkLinesGo(b *testing.B) {
	for range b.N {
		n := 0
		for line := range strings.Lines(linesText) {
			n += len(line)
		}
		sink = n
	}
}
`
	}, "CutErgo", "CutGo", "LinesErgo", "LinesGo")
}
