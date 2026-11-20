package lower

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// TestBoundsFactsAgreeOnIR checks that the IR proves exactly the accesses
// the emitter proves (SLICE-BOUNDS, ir_bounds.go): on the bounds property's
// generated programs (bounds_property_test.go, whose TestBoundsFactsSound
// shows the emitter's unchecked sites sound and complete), each function's
// IR body reads through an unchecked runtime body (ByteAt, StringAt,
// StringN, AddrAt) at the same sites r<N> as the emitter's Go text. A site
// the IR leaves checked costs a check the text path does not pay; a site
// only the IR takes unchecked is unproved. Four programs add the forms the
// generator does not write: a substring of a mapped text (Go's string),
// a let that keeps its facts past a write of what it was read from, a
// range loop whose body writes the subject's index, and a body the IR
// gives up on halfway through an if (its facts must not reach the
// emitter's walk of it).
//
// It catches the ledger's mutants sb-if-facts-dropped, sb-if-facts-leak,
// sb-list-kill-dropped, sb-loop-kill-dropped, sb-while-facts-dropped,
// sb-let-facts-dropped, sb-ir-locals-unstable, sb-index-checked,
// sb-cut-checked, sb-mapped-text-checked, sb-range-kill-dropped and
// sb-fallback-facts-kept.
func TestBoundsFactsAgreeOnIR(t *testing.T) {
	if factLowering == "checked" {
		t.Skip("the reference lowering writes no unchecked site")
	}
	total := 0
	for _, seed := range boundsSeedList() {
		c := boundsProgram(seed)
		var fns []string
		for _, f := range c.fns {
			fns = append(fns, f.name)
		}
		total += boundsAgree(t, fmt.Sprintf("seed %d (replay: %s)", seed, boundsReplay(seed)), c.source(nil), fns, false)
	}
	if total == 0 {
		t.Errorf("the IR takes no site unchecked")
	}
	t.Logf("%d unchecked sites on the IR", total)
	for _, p := range []struct{ name, src string }{
		{"let past a write", `func f1(s string, a0 int, b int) int {
	let mut a = a0
	let mut acc = 0
	if 0 <= a && a < len(s) {
		let m = a
		a = b
		let r1 = s[m]
		acc = acc + int(r1)
	}
	acc + a
}

func main() {
	let _ = f1("abc", 1, 2)
}
`},
		{"range writes the index", `func f1(s string, xs []int, a0 int) int {
	let mut a = a0
	let mut acc = 0
	if 0 <= a && a < len(s) {
		let r2 = s[a]
		acc = int(r2)
		for x in xs {
			let r1 = s[a]
			acc = acc + int(r1)
			a = x
		}
	}
	acc
}

func main() {
	let _ = f1("abc", []int{1, 7}, 0)
}
`},
		{"mapped text", `import fs

func f1(text fs.Text, p int) int {
	let mut acc = 0
	if 0 <= p && p <= len(text)-8 {
		let r1 = text[p : p+8]
		acc = len(r1)
	}
	acc
}

func main() {
	let _ = f1
}
`},
		{"fallback inside an if", `type Ids []int

func f1(s string, xs []int, a int) int {
	let mut acc = 0
	if 0 <= a && a < len(s) {
		let r1 = s[a]
		let ys = Ids(xs)
		acc = int(r1) + ys[0]
	}
	let r2 = s[a]
	acc + int(r2)
}

func main() {
	let _ = f1("abc", []int{1}, 0)
}
`},
	} {
		if n := boundsAgree(t, p.name, p.src, []string{"f1"}, true); n != 1 {
			t.Errorf("%s: %d unchecked sites, want 1 (the program no longer shows its form)", p.name, n)
		}
	}
}

// boundsAgree compares, in src's functions fns, the sites r<N> the emitter
// reads unchecked with those LowerIR does: through the IR body, or the
// emitter's text of a body the IR falls back on (allowed with fallback).
// It answers the number of unchecked sites.
func boundsAgree(t *testing.T, label, src string, fns []string, fallback bool) int {
	t.Helper()
	siteRe := regexp.MustCompile(`^r(\d+)$`)
	unchecked := map[string]bool{"ByteAt": true, "StringAt": true, "StringN": true, "AddrAt": true}
	want := uncheckedSites(mustLower(t, "bounds", src).mainGo())
	files, rep, err := LowerIR(factsUnit(t, src))
	if err != nil {
		t.Fatal(err)
	}
	var ir irFile
	var text strings.Builder
	for _, f := range files {
		switch {
		case f.Path == IRFileName:
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		case strings.HasSuffix(f.Path, ".go"):
			text.Write(f.Data)
			text.WriteString("\n")
		}
	}
	got := map[string]map[int]bool{}
	for _, fn := range ir.Funcs {
		sites := map[int]bool{}
		got[fn.Name] = sites
		irWalk(fn.Body, false, func(s *irStmt, _ bool) {
			if s.K != "def" {
				return
			}
			// The def's local is the site r<N> declared on its line (a
			// def's index counts the parameters too).
			var m []string
			for _, lc := range fn.Locals {
				if lc.Pos[0] == s.Pos[0] && lc.Pos[1] == s.Pos[1] {
					m = siteRe.FindStringSubmatch(lc.Name)
				}
			}
			if m == nil {
				return
			}
			fast := false
			irExprWalkStmts(s.X, false, func(*irStmt, bool) {}, func(e *irExpr, _ bool) {
				fast = fast || e.K == "pcall" && unchecked[e.Name]
			})
			if fast {
				n := 0
				for _, d := range m[1] {
					n = n*10 + int(d-'0')
				}
				sites[n] = true
			}
		}, func(*irExpr, bool) {})
	}
	textSites := uncheckedSites(text.String())
	total := 0
	for _, name := range fns {
		sites, built := got[name]
		if !built {
			if !fallback {
				t.Errorf("%s: %s has no IR body (fallbacks %v)", label, name, rep.Fallback)
				continue
			}
			sites = textSites[name]
		}
		total += len(sites)
		for n := range want[name] {
			if !sites[n] {
				t.Errorf("%s, %s: the emitter proves site r%d, LowerIR checks it\n%s", label, name, n, src)
			}
		}
		for n := range sites {
			if !want[name][n] {
				t.Errorf("%s, %s: LowerIR takes site r%d unchecked, the emitter does not prove it\n%s", label, name, n, src)
			}
		}
	}
	return total
}
