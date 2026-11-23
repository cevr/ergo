package lower

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// hostFileStat reads an open file's metadata and its directory's entries
// as values (S2-FILE-B): (*os.File).Stat, ReadDir and Readdir are bound
// as the methods themselves, whose targets are runtime/v1/hostfs's
// FileStat, FileReadDir and FileReaddir, so a field read needs no row and
// the calls need Ambient alone.
const hostFileStat = `import go os

func sizes(name string, dir string) int fails go.Error needs Ambient {
	let f = try os.Open(name)
	let info = try f.Stat()
	try f.Close()
	let d = try os.Open(dir)
	let es = try d.ReadDir(-1)
	try d.Close()
	let e = try os.Open(dir)
	let is = try e.Readdir(-1)
	try e.Close()
	let mut n = int(info.Size) + len(es) + len(is)
	for x in es {
		if x.IsDir {
			n = n + 1
		}
	}
	for x in is {
		n = n + int(x.Size)
	}
	if info.IsDir {
		n = n + 100
	}
	n
}

func main() needs Console | Ambient {
	let n = sizes("/nonexistent/loop-probe-x", "/nonexistent/loop-probe-y") catch { case go.Error: -1 }
	Console.Println("\(n)")
}
`

// TestFileStatNeedsOnlyAmbient: Stat, ReadDir and Readdir of an open file
// check with Ambient alone, their results are hostfs's structs read by
// field, and the emitted Go type-checks, calling each hostfs function
// with the handle first. Mutants (gobind and syntax mutants.json):
// s2fb-method-via-dropped, s2fb-via-target-refused.
func TestFileStatNeedsOnlyAmbient(t *testing.T) {
	p := lowerSource("hostfilestat", hostFileStat, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("err=%v diagnostics=%v refusals=%v panic=%v\n%s", p.err, p.diags, p.refused, p.panicked, hostFileStat)
	}
	typecheckGo(t, []*lowered{p})
	goSrc := regexp.MustCompile(`/\*line [^*]*\*/`).ReplaceAllString(p.mainGo(), "")
	for _, call := range []string{"FileStat", "FileReadDir", "FileReaddir"} {
		if !regexp.MustCompile(`hostfs\.` + call + `\([a-z_0-9]+[,)]`).MatchString(goSrc) {
			t.Errorf("sizes does not call hostfs.%s with the handle first:\n%s", call, goSrc)
		}
	}
	if strings.Contains(goSrc, ".Stat()") {
		t.Errorf("sizes calls Go's Stat method, whose result is an interface:\n%s", goSrc)
	}
	// The IR emitter's form: a function over a lent handle takes an IR
	// body, whose call is hostfs's function with the handle first.
	const ir = `import go os

func statSize(f mut os.File) int fails go.Error needs Ambient {
	let info = try f.Stat()
	int(info.Size)
}

func main() needs Console | Ambient {
	let f = os.Open("/nonexistent/loop-probe-x") catch { case go.Error: return }
	let n = statSize(f) catch { case go.Error: -1 }
	f.Close() catch { case go.Error: return }
	Console.Println("\(n)")
}
`
	files, rep, err := LowerIR(factsUnit(t, ir))
	if err != nil {
		t.Fatal(err)
	}
	calls, ok := irCallTexts(t, files)["statSize"]
	if !ok {
		t.Fatalf("statSize has no IR body (fallbacks %v)", rep.Fallback)
	}
	handle := regexp.MustCompile(`^FileStat\(local\d+\)$`) // the parameter f, after the environment
	if !slices.ContainsFunc(calls, handle.MatchString) {
		t.Errorf("statSize's IR body calls %v, want FileStat(f)", calls)
	}
}
