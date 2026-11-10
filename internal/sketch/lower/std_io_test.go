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
// Lines loop, then ReadAll, over a pipe.
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
	exes := buildAll(t, []*lowered{mustLower(t, "iorest", rest)}, true)
	if exes == nil {
		return
	}
	for _, c := range []struct{ in, out string }{
		{"a\nb\nc\n", "first=a\nsecond=b\nrest=[c\n]\n"},
		{"a\nb", "first=a\nsecond=b\nrest=[]\n"},
		{"a\n\r\n" + strings.Repeat("w", 9000) + "\nz", "first=a\nsecond=\r\nrest=[" + strings.Repeat("w", 9000) + "\nz]\n"},
	} {
		if stdout, stderr, code := runExeInput(t, exes["iorest"], c.in); code != 0 || stdout != c.out {
			t.Errorf("break then read, stdin %.40q: exit %d, stdout %.40q (want %.40q), stderr %.200q", c.in, code, stdout, c.out, stderr)
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
		"io InputLinesStops":   `InputLinesStops(s string) { let lines = Input{Text: s}.Lines() let mut n = 0 lines(func(line string) bool { n = n + 1 true }) for k in 1..n+2 { let mut calls = 0 lines(func(line string) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
		"io LinesSplitInput":   `LinesSplitInput(s string) { let mut joined = "" let mut first = true let input = Input{Text: s} for line in input.Lines() { if !first { joined = joined + "\n" } joined = joined + line first = false } if strings.HasSuffix(s, "\n") { joined = joined + "\n" } joined == s }`,
		"io PrintsConcatenate": `PrintsConcatenate(a string, b string) { let r = NewRecorder() r.Print(a) r.Eprint(b) r.Print(b) r.Text() == a+b+b }`,
	}, func(key string) string {
		return "Io" + strings.TrimPrefix(key, "io ")
	}, probe, stdout, "IoInputReadAll"))
}
