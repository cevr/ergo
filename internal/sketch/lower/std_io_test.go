package lower

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"
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
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

// TestIORuns (std-align S1b; stdlib.md, io): io.Stdout.Print writes with
// no line feed, Console.Println and io.Stdout keep program order,
// io.Stderr writes standard error, io.Stdin.Lines and ReadAll read
// standard input, and IsTerminal is false on a pipe.
func TestIORuns(t *testing.T) {
	const lines = `import io

func echo() needs Console | io.Stdout | io.Stderr | io.Stdin {
	io.Stdout.Print("a")
	Console.Println("b")
	io.Stderr.Println("e")
	io.Stderr.Print("f")
	for line in io.Stdin.Lines() {
		io.Stdout.Println(line)
	}
	io.Stdout.Println("\(io.Stdout.IsTerminal())")
}

func main() needs Console | Ambient {
	with context { io.Stdout: io.NewHost(), io.Stderr: io.NewErrors(), io.Stdin: io.NewHost() } { echo() }
}
`
	const all = `import io

func main() fails io.ReadFailed needs Console | Ambient {
	let text = with context { io.Stdin: io.NewHost() } { try io.Stdin.ReadAll() }
	Console.Println("[\(text)]")
}
`
	exes := buildAll(t, []*lowered{mustLower(t, "iolines", lines), mustLower(t, "ioall", all)}, true)
	if exes == nil {
		return
	}
	for _, c := range []struct{ in, out string }{
		{"x\ny", "ab\nx\ny\nfalse\n"},
		{"x\ny\n", "ab\nx\ny\nfalse\n"},
		{"", "ab\nfalse\n"},
		{"\n\nz\r\n", "ab\n\n\nz\r\nfalse\n"},
	} {
		stdout, stderr, code := runExeInput(t, exes["iolines"], c.in)
		if code != 0 || stdout != c.out || stderr != "e\nf" {
			t.Errorf("stdin %q: exit %d, stdout %q (want %q), stderr %q (want %q)", c.in, code, stdout, c.out, stderr, "e\nf")
		}
	}
	long := strings.Repeat("w", 200_000)
	if stdout, _, _ := runExeInput(t, exes["iolines"], long+"\nq"); stdout != "ab\n"+long+"\nq\nfalse\n" {
		t.Errorf("a line longer than bufio.Scanner's limit: got %d bytes", len(stdout))
	}
	if stdout, stderr, code := runExeInput(t, exes["ioall"], "x\ny\n"); code != 0 || stdout != "[x\ny\n]\n" {
		t.Errorf("ReadAll: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestIOLaws runs io's laws (stdlib.md, Std laws): PrintsConcatenate on a
// Recorder and LinesSplitInput on an Input, restated as property tests
// over generated strings, and the same on texts with and without a final
// line feed, carriage returns and invalid UTF-8.
func TestIOLaws(t *testing.T) {
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
	r.Print(b)
	assert r.Text() == a + b
	let lines = io.NewRecorder()
	lines.Println(a)
	lines.Print(b)
	assert lines.Text() == a + "\n" + b
}

test IoLinesSplitInput(s string, t string) {
	assert joined(s) == s
	assert joined(s + "\n") == s + "\n"
	assert joined(s + "\n" + t) == s + "\n" + t
	assert joined("\n" + s + "\r\n\n") == "\n" + s + "\r\n\n"
	assert joined("") == ""
	assert joined("\xff\n\xfe") == "\xff\n\xfe"
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
	if code != 0 || !strings.Contains(stdout, "ok IoPrintsConcatenate (100 cases)") || !strings.Contains(stdout, "ok IoLinesSplitInput (100 cases)") {
		t.Errorf("exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
}
