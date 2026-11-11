package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

const wheelSource = `
type Digit int where 0 <= it && it <= 9
type Counter {
	Add(n Digit),
	Total() Digit,
	law AddJoins(a Digit, b Digit) { Add(a); Add(b) } == { Add(plus(a, b)) },
}
type Wheel { at Digit }
func NewWheel() Wheel { Wheel{at: 0} }
func plus(a Digit, b Digit) Digit {
	match (int(a) + int(b)) % 10 {
	case 0: 0
	case 1: 1
	case 2: 2
	case 3: 3
	case 4: 4
	case 5: 5
	case 6: 6
	case 7: 7
	case 8: 8
	case _: 9
	}
}
func Add(w *Wheel, n Digit) { w.at = plus(w.at, n) }
func Total(w *Wheel) Digit { w.at }
func site() Digit {
	let mut w = NewWheel()
	w.Add(1)
	w.Add(2)
	w.Add(4)
	w.Total()
}
func main() needs Console { Console.Println("\(site())") }
`

// TestProvedRunsModel builds Go because the proof must execute the same
// lowered provider bodies that the real build rewrites, not a second model
// implementation. The modulo-ten reference has ten reachable states and
// AddJoins holds on every pair of Digit arguments.
func TestProvedRunsModel(t *testing.T) {
	needsBuild(t)
	savedInline := inlineLowering
	inlineLowering = "call"
	defer func() { inlineLowering = savedInline }()
	src := wheelSource
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v\n%s", diags, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	var receipt string
	ev, err := RewriteChecks(u, func(files []File, laws []string) (string, error) {
		exes := buildAll(t, []*lowered{{name: "proof", src: src, file: file, pkg: pkg, files: files}}, true)
		out, stderr, code := runExeArgs(t, exes["proof"], "--law", "Counter.AddJoins")
		if code != 0 {
			t.Fatalf("proof exit %d:\n%s\n%s", code, out, stderr)
		}
		receipt = out
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receipt, "ok law Counter.AddJoins for Wheel (proved: all 1000 cases)") {
		t.Fatalf("missing exhaustive receipt:\n%s", receipt)
	}
	u.Rewrite = ev
	fast, err := Lower(u)
	if err != nil {
		t.Fatal(err)
	}
	rewriteLowering = "reference"
	ref, err := Lower(u)
	rewriteLowering = ""
	if err != nil {
		t.Fatal(err)
	}
	progs := []*lowered{{name: "fast", src: src, file: file, pkg: pkg, files: fast}, {name: "ref", src: src, file: file, pkg: pkg, files: ref}}
	for _, p := range progs {
		body := goFunc(t, p.mainGo(), "site")
		want := 3
		if p.name == "fast" {
			want = 1
		}
		if got := strings.Count(body, ".Add("); got != want {
			t.Fatalf("%s has %d Add calls, want %d:\n%s", p.name, got, want, body)
		}
	}
	exes := buildAll(t, progs, true)
	for _, name := range []string{"fast", "ref"} {
		out, stderr, code := runExe(t, exes[name])
		if out != "7\n" || code != 0 {
			t.Fatalf("%s: %q, exit %d, stderr %s", name, out, code, stderr)
		}
	}
}
