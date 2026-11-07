package lower

import (
	"strings"
	"testing"
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
	if stdout, stderr, code := runExe(t, exes["text2"]); code != 0 || stdout != want {
		t.Errorf("exit %d\nstdout %q\nwant   %q\nstderr %s", code, stdout, want, firstLines(stderr, 10))
	}
	stdout, stderr, code := runExeArgs(t, exes["text2laws"], "--seed", "1")
	if code != 0 || !strings.Contains(stdout, "ok StringsCutRebuilds (100 cases)") || !strings.Contains(stdout, "ok StringsLinesRebuild (100 cases)") {
		t.Errorf("the laws: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 10))
	}
}
