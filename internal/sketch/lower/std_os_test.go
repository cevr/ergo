package lower

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// osProgram reads the host process through os.Process: the count and the
// arguments, a set, an empty and an unset variable; it fails os.Exit when
// the first argument is "nope" or "zero".
const osProgram = `import os
import strings

func show(v Option[string]) string {
	match v {
	case Some[string] as s:
		"Some \(s.Value)"
	case None:
		"None"
	}
}

func run() fails os.Exit needs Console | os.Process {
	let args = os.Process.Args()
	if len(args) > 0 && args[0] == "nope" {
		return os.Exit{Code: 3, Message: "[NOT_FOUND] Unknown topic 'nope'"}
	}
	if len(args) > 0 && args[0] == "zero" {
		return os.Exit{Code: len(args) - 1, Message: "claims success"} // computed: a constant 0 is S108 (G-ex-c-17)
	}
	Console.Println("\(len(args)) \(strings.Join(args, " "))")
	Console.Println(show(os.Process.Getenv("ERGO_G2_K")))
	Console.Println(show(os.Process.Getenv("ERGO_G2_EMPTY")))
	Console.Println(show(os.Process.Getenv("ERGO_G2_UNSET")))
}

func main() fails os.Exit needs Console | Ambient {
	let live = context { os.Process: os.NewHost() }
	with live { try run() }
}
`

// fakeProgram binds a program's own Process provider: the same reads
// through a memory environment.
const fakeProgram = `import os
import strings

type FakeEnv { Vars Map[string, string], Argv []string }

func Args(e FakeEnv) []string { e.Argv }

func Getenv(e FakeEnv, key string) Option[string] { e.Vars.Get(key) }

func show(v Option[string]) string {
	match v {
	case Some[string] as s:
		"Some \(s.Value)"
	case None:
		"None"
	}
}

func run() needs Console | os.Process {
	let args = os.Process.Args()
	Console.Println("\(len(args)) \(strings.Join(args, " "))")
	Console.Println(show(os.Process.Getenv("ERGO_G2_K")))
	Console.Println(show(os.Process.Getenv("ERGO_G2_EMPTY")))
	Console.Println(show(os.Process.Getenv("ERGO_G2_UNSET")))
}

func main() needs Console {
	let env = FakeEnv{Vars: Map[string, string]{"ERGO_G2_K": "v", "ERGO_G2_EMPTY": ""}, Argv: []string{"a", "b"}}
	with context { os.Process: env } { run() }
}
`

// paramProgram passes os.Host and a program's own provider as values of
// os.Process, and calls the operations on the parameter.
const paramProgram = `import os

type FakeEnv { Argv []string }

func Args(e FakeEnv) []string { e.Argv }

func Getenv(_ FakeEnv, key string) Option[string] { Some[string]{Value: "fake"} }

func show(p os.Process) string {
	match p.Getenv("ERGO_G2_K") {
	case Some[string] as s:
		"\(len(p.Args())) \(s.Value)"
	case None:
		"\(len(p.Args())) None"
	}
}

func main() needs Console | Ambient {
	Console.Println(show(os.NewHost()))
	let p os.Process = FakeEnv{Argv: []string{"x", "y", "z"}}
	Console.Println(show(p))
}
`

// TestOSProcessRuns: std os reads the host process through os.Host bound in
// a context (stdlib.md, host packages): Args excludes the program name;
// Getenv tells Some("") from None; os.Exit alone writes its Message
// verbatim and exits with its Code, and a Code outside 1..125 exits 1
// (runtime.md, Exit causes). A program's own provider gives the same
// output through the same calls. os.Process is an open std protocol
// (check.SealedStd), so both providers convert to it and it lowers as a
// declaration of the program.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestOSProcessRuns(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "osHost", "osFake", "osParam")
	if exes == nil {
		return
	}
	const want = "2 a b\nSome v\nSome \nNone\n"
	env := []string{"ERGO_G2_K=v", "ERGO_G2_EMPTY="}
	cases := []struct {
		name, exe string
		args      []string
		stdout    string
		stderr    string
		exit      int
	}{
		{"host", exes["osHost"], []string{"a", "b"}, want, "", 0},
		{"hostNope", exes["osHost"], []string{"nope"}, "", "[NOT_FOUND] Unknown topic 'nope'\n", 3},
		{"hostZero", exes["osHost"], []string{"zero"}, "", "claims success\nerror: os.Exit code 0 is outside 1..125\n", 1},
		{"fake", exes["osFake"], []string{"ignored"}, want, "", 0},
		{"param", exes["osParam"], []string{"a"}, "1 v\n3 fake\n", "", 0},
	}
	for _, c := range cases {
		stdout, stderr, code := runWithEnv(t, c.exe, env, c.args...)
		if stdout != c.stdout || stderr != c.stderr || code != c.exit {
			t.Errorf("%s: exit %d, want %d\nstdout %q\nwant   %q\nstderr %q\nwant   %q", c.name, code, c.exit, stdout, c.stdout, stderr, c.stderr)
		}
	}
}

// runWithEnv runs exe with args and with env added to an environment that
// holds no ERGO_G2_ variable of the test's own.
func runWithEnv(t *testing.T, exe string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ERGO_G2_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
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
