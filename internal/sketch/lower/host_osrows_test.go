package lower

import "testing"

// hostOSRows changes the working directory and a file's owner by path
// with only Ambient (S2-FILE's follow-up): os.Chdir, os.Chown and
// os.Lchown are host rows, as (*os.File).Chdir and Chown are.
const hostOSRows = `import go os

func move(dir string, name string) fails go.Error needs Ambient {
	try os.Chown(name, -1, -1)
	try os.Lchown(name, -1, -1)
	try os.Chdir(dir)
}

func main() needs Console | Ambient {
	move("/nonexistent/loop-probe-x", "/nonexistent/loop-probe-y") catch { case go.Error: Console.Println("no") }
}
`

// TestOSPathRowsNeedOnlyAmbient: os.Chdir, os.Chown and os.Lchown check
// with Ambient alone (no go.Foreign), and the emitted Go type-checks.
// Mutant (gofacts mutants.json): osr-chown-row-dropped.
func TestOSPathRowsNeedOnlyAmbient(t *testing.T) {
	p := lowerSource("hostosrows", hostOSRows, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("err=%v diagnostics=%v refusals=%v panic=%v\n%s", p.err, p.diags, p.refused, p.panicked, hostOSRows)
	}
	typecheckGo(t, []*lowered{p})
}
