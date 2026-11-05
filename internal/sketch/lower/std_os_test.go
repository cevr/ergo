package lower

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// osPart2 names the environment variable that runs the os tests before G2
// part 2 lands. Part 1 (runtime/v1/host.go, std/os) is in; part 2 adds the
// sealed rule (check/extern.go sealedStd, lower/std.go runtimeType;
// stdlib.md, Types 3). Until then a program value does not convert to
// os.Process (S109), so these tests skip. Part 2 removes the skip.
const osPart2 = "ERGO_G2_PART2"

func skipBeforePart2(t *testing.T) {
	t.Helper()
	if os.Getenv(osPart2) == "" {
		t.Skip("G2 part 2: os.Process is sealed until check.SealedStd lands; set " + osPart2 + "=1 to run")
	}
}

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
		return os.Exit{Code: 0, Message: "claims success"}
	}
	Console.Println("\(len(args)) \(strings.Join(args, " "))")
	Console.Println(show(os.Process.Getenv("ERGO_G2_K")))
	Console.Println(show(os.Process.Getenv("ERGO_G2_EMPTY")))
	Console.Println(show(os.Process.Getenv("ERGO_G2_UNSET")))
}

let Live = context { os.Process: os.Host }

func main() fails os.Exit needs Console {
	with Live { try run() }
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

// TestOSProcessRuns: std os reads the host process through os.Host bound in
// a context (stdlib.md, host packages): Args excludes the program name;
// Getenv tells Some("") from None; os.Exit alone writes its Message
// verbatim and exits with its Code, and a Code outside 1..125 exits 1
// (runtime.md, Exit causes). A program's own provider gives the same
// output through the same calls.
func TestOSProcessRuns(t *testing.T) {
	skipBeforePart2(t)
	host := mustLower(t, "osHost", osProgram)
	fake := mustLower(t, "osFake", fakeProgram)
	exes := buildAll(t, []*lowered{host, fake}, true)
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
	}
	for _, c := range cases {
		stdout, stderr, code := runWithEnv(t, c.exe, env, c.args...)
		if stdout != c.stdout || stderr != c.stderr || code != c.exit {
			t.Errorf("%s: exit %d, want %d\nstdout %q\nwant   %q\nstderr %q\nwant   %q", c.name, code, c.exit, stdout, c.stdout, stderr, c.stderr)
		}
	}
}

// TestOSProcessIsNotImplicit: the entry provides only Console, so main
// that names os.Process in its row is S109 (stdlib.md, host packages).
func TestOSProcessIsNotImplicit(t *testing.T) {
	o := lowerSource("osEntry", `import os

func main() needs Console | os.Process {
	Console.Println("\(len(os.Process.Args()))")
}
`, true)
	if len(o.diags) != 0 || len(o.refused) != 1 || !strings.Contains(o.refused[0].Message, "main needs os.Process, and the host provides only Console") {
		t.Fatalf("want one entry refusal naming os.Process and the host's Console; got diags %v refused %+v err %v panicked %v", o.diags, o.refused, o.err, o.panicked)
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
