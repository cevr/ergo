package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The call sites' seeded property (#196 item 13; types.md, Call sites).
// Each seed writes a program whose main binds the results of calls that
// leave out, or write, a final CallSite parameter, at lines and columns the
// seed varies (blank and comment lines, indentation, spaces after `=`).
// The calls reach the parameter through each callee rule 2 names: a
// declared function, a dot call, an operation of an operation-type value,
// a capability's operation (directly, and through a function that needs
// it), a generic function, a function value, a function literal and a call
// with a trailing block; a nested call, which has a site of its own; a
// helper that forwards its caller's site (rule 3) and one that leaves it
// out inside its body; and a written CallSite literal. The generator is the
// model: it knows the byte offset where it writes each call, so it writes
// the line and column each call must print.
//
// Replay: go test -run '^TestCallSitesAgree$' ./internal/sketch/lower -args -site.seed=N

var siteSeed = flag.Uint64("site.seed", 0, "run TestCallSitesAgree on this seed alone")

// siteHelpers declares the callees. inner's and viaCap's own calls sit at
// the lines and columns siteFixed names (tab-indented, column 2).
const siteHelpers = `// law-exempt: the property's operation type.
type Tagger {
	Tag(note string, site CallSite) string,
}

type Plain {}

func Tag(p Plain, note string, site CallSite) string {
	"\(note) \(site.Line):\(site.Column)"
}

func at(note string, site CallSite) string {
	"\(note) \(site.Line):\(site.Column)"
}

func fwd(note string, site CallSite) string {
	at(note, site)
}

func inner(note string, site CallSite) string {
	at(note)
}

func viaCap(note string) string needs Tagger {
	Tagger.Tag(note)
}

func gen[T](x T, show func(v T) string, site CallSite) string {
	"\(show(x)) \(site.Line):\(site.Column)"
}

func each(note string, f func() string, site CallSite) string {
	"\(note)\(f()) \(site.Line):\(site.Column)"
}

func file(site CallSite) string {
	site.File
}

func show(s string) string {
	s
}

`

// siteFixed are the sites of the calls inside inner and viaCap, by the
// form that reaches them.
var siteFixed = map[string]string{"inner": "21:2", "capability": "25:2"}

// siteKinds are the paths the fixed seeds must reach, one per call form.
var siteKinds = []string{
	"declared", "dot", "operation", "provider", "capability", "generic", "value",
	"literal", "trailing", "nested", "forward", "inner", "written",
}

// siteProgram writes the program of seed and the output it must print.
func siteProgram(seed uint64, reached map[string]bool) (string, string) {
	r := rand.New(rand.NewPCG(seed, 0x5173))
	var b, want strings.Builder
	b.WriteString(siteHelpers)
	b.WriteString("func main() needs Console {\n")
	b.WriteString("\tlet p = Plain\n\tlet t Tagger = Plain\n\tlet fv = at\n")
	b.WriteString("\tlet lit = func(note string, site CallSite) string { at(note, site) }\n")
	b.WriteString("\tConsole.Println(\"\\(file() == file()) \\(len(file()) > 0)\")\n")
	want.WriteString("true true\n")
	line := strings.Count(b.String(), "\n") + 1
	// emit writes one line and reports the column where call starts in it.
	emit := func(text, call string) int {
		col := strings.Index(text, call) + 1
		b.WriteString(text + "\n")
		line++
		return col
	}
	n := 8 + r.IntN(8)
	for i := 0; i < n || !allReached(reached) && i < 4*len(siteKinds); i++ {
		for range r.IntN(3) {
			if r.IntN(2) == 0 {
				emit("", "")
			} else {
				emit("\t// a comment line", "")
			}
		}
		indent := strings.Repeat("\t", 1+r.IntN(2)) + strings.Repeat(" ", r.IntN(3))
		pad := strings.Repeat(" ", r.IntN(4))
		kind := siteKinds[r.IntN(len(siteKinds))]
		if i < len(siteKinds) && seed == 1 {
			kind = siteKinds[i] // seed 1 visits every form in order
		}
		reached[kind] = true
		note := fmt.Sprintf("k%d", i)
		v := fmt.Sprintf("v%d", i)
		var call string
		switch kind {
		case "declared":
			call = fmt.Sprintf("at(%q)", note)
		case "dot":
			call = fmt.Sprintf("p.Tag(%q)", note)
		case "operation":
			call = fmt.Sprintf("t.Tag(%q)", note)
		case "capability":
			call = fmt.Sprintf("viaCap(%q)", note)
		case "generic":
			call = fmt.Sprintf("gen(%q, show)", note)
		case "value":
			call = fmt.Sprintf("fv(%q)", note)
		case "literal":
			call = fmt.Sprintf("lit(%q)", note)
		case "trailing":
			call = fmt.Sprintf("each(%q) { \"!\" }", note)
		case "nested":
			call = fmt.Sprintf("at(at(%q))", note)
		case "forward":
			call = fmt.Sprintf("fwd(%q)", note)
		case "inner":
			call = fmt.Sprintf("inner(%q)", note)
		case "written":
			call = fmt.Sprintf("at(%q, CallSite{File: \"w\", Line: %d, Column: %d})", note, 100+i, 1+r.IntN(9))
		case "provider":
			emit(indent+"with context { Tagger: Plain } {", "")
			text := indent + "\tConsole.Println(" + pad + fmt.Sprintf("Tagger.Tag(%q)", note) + ")"
			col := emit(text, "Tagger.Tag(")
			want.WriteString(fmt.Sprintf("%s %d:%d\n", note, line-1, col))
			emit(indent+"}", "")
			continue
		}
		text := indent + "let " + v + " =" + pad + " " + call
		if kind == "capability" {
			text = indent + "let " + v + " =" + pad + " with context { Tagger: Plain } { " + call + " }"
		}
		col := emit(text, call)
		at := fmt.Sprintf("%d:%d", line-1, col)
		switch kind {
		case "trailing":
			want.WriteString(note + "! " + at + "\n")
		case "nested":
			want.WriteString(fmt.Sprintf("%s %d:%d %s\n", note, line-1, col+3, at))
		case "inner", "capability":
			want.WriteString(note + " " + siteFixed[kind] + "\n")
		case "written":
			var l, c int
			fmt.Sscanf(call[strings.Index(call, "Line: "):], "Line: %d, Column: %d", &l, &c)
			want.WriteString(fmt.Sprintf("%s %d:%d\n", note, l, c))
		default:
			want.WriteString(note + " " + at + "\n")
		}
		emit(indent+"Console.Println("+v+")", "")
	}
	// Every binding is used, whichever forms the seed drew.
	b.WriteString("\tlet z = CallSite{File: \"\", Line: 0, Column: 0}\n")
	b.WriteString("\tConsole.Println(\"\\(p.Tag(\"e\", z))|\\(t.Tag(\"e\", z))|\\(fv(\"e\", z))|\\(lit(\"e\", z))\")\n")
	want.WriteString("e 0:0|e 0:0|e 0:0|e 0:0\n")
	b.WriteString("}\n")
	return b.String(), want.String()
}

// allReached reports whether reached holds every call form.
func allReached(reached map[string]bool) bool {
	for _, k := range siteKinds {
		if !reached[k] {
			return false
		}
	}
	return true
}

// TestCallSitesAgree is the seeded property (see above).
// It builds Go because the property is about the value a callee receives
// when the program runs: the checker records only that a call leaves the site out,
// and the lowered literal is seen as the callee's value only by running it.
func TestCallSitesAgree(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if *siteSeed != 0 {
		seeds = []uint64{*siteSeed}
	}
	reached := map[string]bool{}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := siteProgram(seed, reached)
		programs = append(programs, mustLower(t, fmt.Sprintf("site%d", seed), src))
		runs = append(runs, run{seed, src, want})
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the call-site programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[fmt.Sprintf("site%d", r.seed)])
		if out != r.want || code != 0 {
			t.Errorf("seed %d: the program and the call-site model differ\nReplay: go test -run '^TestCallSitesAgree$' ./internal/sketch/lower -args -site.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, r.want)
		}
	}
	if *siteSeed == 0 && !allReached(reached) {
		t.Errorf("the fixed seeds never reach every call form: %v", reached)
	}
}
