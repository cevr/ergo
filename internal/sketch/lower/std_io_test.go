package lower

import (
	"bytes"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// runExeInput runs a built program with stdin and args, and returns its
// stdout, stderr and exit code.
func runExeInput(t *testing.T, exe, stdin string, args ...string) (string, string, int) {
	t.Helper()
	probeRan(exe)
	cmd := exec.Command(exe, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd)
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

// TestIOStdinRest (S1b round 2, M2): a break out of io.Stdin.Lines leaves
// the rest of the input for the next read: a header line, then a second
// Lines loop, then ReadAll (or In's Source chunks until None, SJ0), over a
// pipe.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestIOStdinRest(t *testing.T) {
	needsBuild(t)
	const rest = `import io

func main() fails io.ReadFailed needs Console | Ambient {
	with context { io.Stdin: io.NewStdin() } {
		for line in io.Stdin.Lines() {
			Console.Println("first=\(line)")
			break
		}
		for line in io.Stdin.Lines() {
			Console.Println("second=\(line)")
			break
		}
		let text = try io.Stdin.ReadAll()
		Console.Println("rest=[\(text)]")
	}
}
`
	// The same reads, with the rest taken as In's Source chunks (SJ0):
	// they go on where Lines stopped and end with None.
	const chunks = `import io

func rest(src io.Source) string fails io.ReadFailed {
	let mut out = ""
	for {
		match try src.Read() {
		case Some[string] as c:
			out = out + c.Value
		case None:
			break
		}
	}
	out
}

func main() fails io.ReadFailed needs Console | Ambient {
	let stdin = io.NewStdin()
	with context { io.Stdin: stdin } {
		for line in io.Stdin.Lines() {
			Console.Println("first=\(line)")
			break
		}
		for line in io.Stdin.Lines() {
			Console.Println("second=\(line)")
			break
		}
	}
	let text = try rest(stdin)
	Console.Println("rest=[\(text)]")
}
`
	exes := buildAll(t, []*lowered{mustLower(t, "iorest", rest), mustLower(t, "iochunks", chunks)}, true)
	if exes == nil {
		return
	}
	for _, c := range []struct{ in, out string }{
		{"a\nb\nc\n", "first=a\nsecond=b\nrest=[c\n]\n"},
		{"a\nb", "first=a\nsecond=b\nrest=[]\n"},
		{"a\n\r\n" + strings.Repeat("w", 99000) + "\nz", "first=a\nsecond=\r\nrest=[" + strings.Repeat("w", 99000) + "\nz]\n"},
	} {
		for _, exe := range []string{"iorest", "iochunks"} {
			if stdout, stderr, code := runExeInput(t, exes[exe], c.in); code != 0 || stdout != c.out {
				t.Errorf("%s: break then read, stdin %.40q: exit %d, stdout %.40q (want %.40q), stderr %.200q", exe, c.in, code, stdout, c.out, stderr)
			}
		}
	}
}

// TestIOStreamsAreDistinct (S1b round 2, M1): a Stdout provider is not a
// Stderr provider, and a Stderr provider is not a Stdout provider, so
// binding the wrong stream is a check error (S109 at the binding), not a
// write to the wrong stream. One Recorder still serves both.
func TestIOStreamsAreDistinct(t *testing.T) {
	for _, c := range []struct{ name, binding, want string }{
		{"stderr-host", "io.Stdout: io.NewHost(), io.Stderr: io.NewHost()", "io.Host cannot provide io.Stderr"},
		{"stdout-errors", "io.Stdout: io.NewErrors(), io.Stderr: io.NewErrors()", "io.Errors cannot provide io.Stdout"},
	} {
		src := `import io

func main() needs Ambient {
	with context { ` + c.binding + ` } { use() }
}

func use() needs io.Stdout | io.Stderr { }
`
		o := lowerSource(c.name, src, true)
		var codes []string
		for _, d := range o.diags {
			codes = append(codes, d.Code+" line "+strconv.Itoa(d.Line)+": "+d.Message)
		}
		if len(o.diags) != 1 || o.diags[0].Code != "S106" || o.diags[0].Line != 4 || !strings.Contains(o.diags[0].Message, c.want) {
			t.Errorf("%s: want one S106 at the binding (line 4) with %q; got %q", c.binding, c.want, codes)
		}
	}
	mustLower(t, "recorder-both", `import io

func use() needs io.Stdout | io.Stderr {
	io.Stdout.Print("o")
	io.Stderr.Eprint("e")
}

func main() needs Console {
	let r = io.NewRecorder()
	with context { io.Stdout: r, io.Stderr: r } { use() }
	Console.Println(r.Text())
}
`)
}

// TestIOLaws runs io's laws (stdlib.md, Std laws): PrintsConcatenate on a
// Recorder and LinesSplitInput on an Input, restated as property tests
// over generated strings, and the same on texts with and without a final
// line feed, carriage returns and invalid UTF-8.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestIOLaws(t *testing.T) {
	needsBuild(t)
	const probe = `import io
import slices
import strings

func joined(s string) string {
	let mut out = ""
	let mut first = true
	let input = io.Input{Text: s}
	for line in input.Lines() {
		if !first {
			out = out + "\n"
		}
		out = out + line
		first = false
	}
	if strings.HasSuffix(s, "\n") {
		out = out + "\n"
	}
	out
}

test IoPrintsConcatenate(a string, b string) {
	let r = io.NewRecorder()
	r.Print(a)
	r.Eprint(b)
	r.Print(b)
	assert r.Text() == a + b + b
	let lines = io.NewRecorder()
	lines.Println(a)
	lines.Eprintln(b)
	lines.Print(b)
	assert lines.Text() == a + "\n" + b + "\n" + b
}

test IoLinesSplitInput(s string, t string) {
	assert joined(s) == s
	assert joined(s + "\n") == s + "\n"
	assert joined(s + "\n" + t) == s + "\n" + t
	assert joined("\n" + s + "\r\n\n") == "\n" + s + "\r\n\n"
	assert joined("") == ""
	assert joined("\xff\n\xfe") == "\xff\n\xfe"
}

// lineCount is the count of lines of s: its line feeds, and one more for
// a last line with no line feed.
func lineCount(s string) int {
	if s == "" || strings.HasSuffix(s, "\n") {
		return strings.Count(s, "\n")
	}
	strings.Count(s, "\n") + 1
}

test IoInputLinesStops(s string, t string) {
	for text in []string{s, s + "\n" + t, "\n" + s + "\r\n\n" + t + "\n", "", "\n", "\xff\n\xfe"} {
		let lines = io.Input{Text: text}.Lines()
		let n = lineCount(text)
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

test IoInputReadAll(s string) {
	assert try io.Input{Text: s}.ReadAll() == s
}

// pieces is the chunks of src joined by "|", read until None.
func pieces(src io.Source) string fails io.ReadFailed {
	let mut out = ""
	for {
		match try src.Read() {
		case Some[string] as c:
			out = out + c.Value + "|"
		case None:
			break
		}
	}
	out
}

test IoChunksJoinToInput(text string, cuts []int, size int) {
	for t in []string{text, "", "a\xff\u00e9" + text} {
		let mut kept = []int{}
		for c in cuts {
			if c > 0 && c < len(t) && (len(kept) == 0 || c > kept[len(kept)-1]) {
				kept = kept.Append(c)
			}
		}
		let mut joined = ""
		let mut ends = []int{}
		let m = io.CutAt(t, cuts)
		for {
			match try m.Read() {
			case Some[string] as c:
				assert c.Value != ""
				joined = joined + c.Value
				ends = ends.Append(len(joined))
			case None:
				break
			}
		}
		assert try m.Read() == None
		let mut want = kept
		if len(t) > 0 {
			want = want.Append(len(t))
		}
		assert joined == t && slices.Equal(ends, want)
		let n = if size < 0 { -(size % 9) + 1 } else { size % 9 + 1 }
		let mut sized = ""
		let mut count = 0
		let chunked = io.Chunked(t, n)
		for {
			match try chunked.Read() {
			case Some[string] as c:
				assert c.Value != "" && len(c.Value) <= n
				assert len(c.Value) == n || len(sized)+len(c.Value) == len(t)
				sized = sized + c.Value
				count = count + 1
			case None:
				break
			}
		}
		assert sized == t && count == (len(t)+n-1)/n
	}
	assert try pieces(io.CutAt("abcdef", []int{2, 2, 9, 4, 1})) == "ab|cd|ef|"
	assert try pieces(io.Chunked("abc", 0)) == "abc|"
}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "iolaws", probe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["iolaws"], "--seed", "1")
	if code != 0 {
		t.Errorf("exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, map[string]string{
		"io ChunksJoinToInput": `ChunksJoinToInput(text string, cuts []int, size int) { let mut kept = []int{} for c in cuts { if c > 0 && c < len(text) && (len(kept) == 0 || c > kept[len(kept)-1]) { kept = kept.Append(c) } } let mut joined = "" let mut ends = []int{} let m = CutAt(text, cuts) for { match try m.Read() { case Some[string] as c: if c.Value == "" { return false } joined = joined + c.Value ends = ends.Append(len(joined)) case None: break } } let again = try m.Read() let mut want = kept if len(text) > 0 { want = want.Append(len(text)) } let n = if size < 0 { -(size % 9) + 1 } else { size%9 + 1 } let mut sized = "" let mut pieces = 0 let chunked = Chunked(text, n) for { match try chunked.Read() { case Some[string] as c: if c.Value == "" || len(c.Value) > n || (len(c.Value) < n && len(sized)+len(c.Value) != len(text)) { return false } sized = sized + c.Value pieces = pieces + 1 case None: break } } joined == text && again == None && slices.Equal(ends, want) && sized == text && pieces == (len(text)+n-1)/n }`,
		"io InputLinesStops":   `InputLinesStops(s string) { let lines = Input{Text: s}.Lines() let mut n = 0 lines(func(line string) bool { n = n + 1 true }) for k in 1..n+2 { let mut calls = 0 lines(func(line string) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
		"io LinesSplitInput":   `LinesSplitInput(s string) { let mut joined = "" let mut first = true let input = Input{Text: s} for line in input.Lines() { if !first { joined = joined + "\n" } joined = joined + line first = false } if strings.HasSuffix(s, "\n") { joined = joined + "\n" } joined == s }`,
		"io PrintsConcatenate": `PrintsConcatenate(a string, b string) { let r = NewRecorder() r.Print(a) r.Eprint(b) r.Print(b) r.Text() == a+b+b }`,
	}, func(key string) string {
		return "Io" + strings.TrimPrefix(key, "io ")
	}, probe, stdout, "IoInputReadAll"))
}
