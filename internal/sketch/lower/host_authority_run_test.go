package lower

import (
	"os"
	"path/filepath"
	"testing"
)

// hostAuthorityProgram is host-authority H1's P1 (stdlib.md, Host
// packages): main lists Ambient, the entry binds it, and main chooses both
// hosts with NewHost in its context. It prints the file its first
// argument names and the variable ERGO_G2_K.
const hostAuthorityProgram = `import fs
import os

func run() needs Console | fs.Files | os.Process {
	let path = os.Process.Args()[0]
	let text = fs.Files.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return
	}
	Console.Println(text)
	match os.Process.Getenv("ERGO_G2_K") {
	case Some[string] as s:
		Console.Println(s.Value)
	case None:
		Console.Println("unset")
	}
}

func main() needs Console | Ambient {
	with context { fs.Files: fs.NewHost(), os.Process: os.NewHost() } { run() }
}
`

// TestHostAuthorityRuns: a main that needs Ambient builds and reads the
// host through the providers it made (exit 0), and the entry provides
// Console and Ambient only, so a main that needs os.Process or Clock is
// S109 naming both.
func TestHostAuthorityRuns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	if err := os.WriteFile(path, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := mustLower(t, "hostAuthority", hostAuthorityProgram)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	stdout, stderr, code := runWithEnv(t, exes["hostAuthority"], []string{"ERGO_G2_K=v"}, path)
	if stdout != "alpha\nv\n" || stderr != "" || code != 0 {
		t.Errorf("exit %d, want 0\nstdout %q, want %q\nstderr %q", code, stdout, "alpha\nv\n", stderr)
	}
	for _, c := range []struct{ name, head, row, cap string }{
		{"process", "import os\n\n", "Console | os.Process", "os.Process"},
		{"clock", "import time\n\n", "Console | time.Clock", "time.Clock"},
	} {
		// A refusal of the entry is an S109 (Refused).
		o := lowerSource("hostEntry_"+c.name, c.head+"func main() needs "+c.row+" {\n\tConsole.Println(\"x\")\n}\n", true)
		want := "main needs " + c.cap + ", and the host provides only Console and Ambient; bind it with a with block inside main"
		if len(o.diags) != 0 || len(o.refused) != 1 || o.refused[0].Message != want {
			t.Errorf("%s: want one S109 %q; got diags %v refused %+v err %v panicked %v", c.name, want, o.diags, o.refused, o.err, o.panicked)
		}
	}
}
