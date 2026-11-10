package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
)

// The context values' seeded property (L5, #237 stage 2; rows.md, Context
// values). Each seed writes a program over k capabilities C0…Ck-1, each
// `Get() int`, which one provider Box implements. It makes Context values
// in every form a context converts from: an inline literal, a local
// context, a named context, a function's result and a generic function's
// pass-through, each to a random row R of the capabilities its context
// binds, the context binding one more than R where it can. main then nests
// withs of those values and of inline contexts, and in each frame prints
// Ci.Get() of each capability the frame binds, and calls a generic
// runner `Run[R](w Context[R], f func() int needs R)`, whose written row
// is empty, so its with must discharge R, with a literal that reads R.
// The outer context binds every capability. The generator is the model: it knows which box each capability
// holds in each frame. A with of a value that wrote outside its copy of
// the environment, a value that installed more than R, a row-parameter
// discharge that missed, or a row argument the parser split, prints other
// numbers or does not build.
//
// Replay: go test -run '^TestContextValuesAgree$' ./internal/sketch/lower -args -ctxval.seed=N

var ctxvalSeed = flag.Uint64("ctxval.seed", 0, "run TestContextValuesAgree on this seed alone")

// ctxvalProgram writes the program of seed and the output its model predicts.
func ctxvalProgram(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, 0x5c7a))
	k := 2 + r.IntN(3)
	var b, decls, body, out strings.Builder
	b.WriteString("type Box { n int }\n")
	for i := 0; i < k; i++ {
		fmt.Fprintf(&b, "\n// law-exempt: the property's operation types.\ntype C%d {\n\tGet() int,\n}\n", i)
	}
	b.WriteString("\nfunc Get(b Box) int { b.n }\n")
	b.WriteString("\nfunc pass[R](w Context[R]) Context[R] { w }\n")
	b.WriteString("\nfunc Run[R](w Context[R], f func() int needs R) int {\n\twith w { f() }\n}\n")
	next := 100
	box := func() int { next++; return next }
	// subset is a random nonempty subset of the capabilities, in order.
	subset := func() []int {
		var s []int
		for i := 0; i < k; i++ {
			if r.IntN(2) == 0 {
				s = append(s, i)
			}
		}
		if len(s) == 0 {
			s = []int{r.IntN(k)}
		}
		return s
	}
	row := func(caps []int) string {
		names := make([]string, len(caps))
		for i, c := range caps {
			names[i] = fmt.Sprintf("C%d", c)
		}
		return strings.Join(names, " | ")
	}
	// literal writes a context literal binding caps, and the first
	// capability not in caps, returning the box of each capability it binds.
	literal := func(caps []int) (string, map[int]int) {
		bound := map[int]int{}
		all := append([]int(nil), caps...)
		for extra := 0; extra < k; extra++ {
			if !ctxvalHas(all, extra) {
				all = append(all, extra)
				break
			}
		}
		var entries []string
		for _, c := range all {
			n := box()
			bound[c] = n
			entries = append(entries, fmt.Sprintf("C%d: Box{n: %d}", c, n))
		}
		return "context { " + strings.Join(entries, ", ") + " }", bound
	}
	// The values: name -> the boxes it installs (R only).
	nv := 2 + r.IntN(3)
	values := make([]map[int]int, nv)
	for v := 0; v < nv; v++ {
		caps := subset()
		lit, bound := literal(caps)
		inst := map[int]int{}
		for _, c := range caps {
			inst[c] = bound[c]
		}
		values[v] = inst
		ty := "Context[" + row(caps) + "]"
		switch r.IntN(5) {
		case 0: // inline literal
			fmt.Fprintf(&decls, "\tlet v%d %s = %s\n", v, ty, lit)
		case 1: // local context
			fmt.Fprintf(&decls, "\tlet l%d = %s\n\tlet v%d %s = l%d\n", v, lit, v, ty, v)
		case 2: // named context
			fmt.Fprintf(&b, "\nlet N%d = %s\n", v, lit)
			fmt.Fprintf(&decls, "\tlet v%d %s = N%d\n", v, ty, v)
		case 3: // a function's result
			fmt.Fprintf(&b, "\nfunc mk%d() %s { %s }\n", v, ty, lit)
			fmt.Fprintf(&decls, "\tlet v%d = mk%d()\n", v, v)
		default: // through a generic function
			fmt.Fprintf(&decls, "\tlet v%d = pass(%s(%s))\n", v, "mkp"+fmt.Sprint(v), "")
			fmt.Fprintf(&b, "\nfunc mkp%d() %s { %s }\n", v, ty, lit)
		}
	}
	var frame func(depth int, bound map[int]int)
	frame = func(depth int, bound map[int]int) {
		indent := strings.Repeat("\t", depth)
		var held []int
		for c := range bound {
			held = append(held, c)
		}
		sort.Ints(held)
		for _, c := range held {
			fmt.Fprintf(&body, "%sConsole.Println(\"\\(C%d.Get())\")\n", indent, c)
			fmt.Fprintf(&out, "%d\n", bound[c])
		}
		if depth > 3 {
			return
		}
		for i := 1 + r.IntN(2); i > 0; i-- {
			inner := map[int]int{}
			for c, n := range bound {
				inner[c] = n
			}
			if r.IntN(4) == 0 {
				lit, lb := literal(subset())
				for c, n := range lb {
					inner[c] = n
				}
				fmt.Fprintf(&body, "%swith %s {\n", indent, lit)
			} else {
				v := r.IntN(nv)
				for c, n := range values[v] {
					inner[c] = n
				}
				if r.IntN(3) == 0 {
					// The generic runner, with a literal that reads R.
					var caps []int
					for c := range values[v] {
						caps = append(caps, c)
					}
					sort.Ints(caps)
					sum, terms := 0, make([]string, len(caps))
					for j, c := range caps {
						sum += values[v][c]
						terms[j] = fmt.Sprintf("C%d.Get()", c)
					}
					fmt.Fprintf(&body, "%sConsole.Println(\"\\(Run(v%d, func() int { %s }))\")\n", indent, v, strings.Join(terms, " + "))
					fmt.Fprintf(&out, "%d\n", sum)
					continue
				}
				fmt.Fprintf(&body, "%swith v%d {\n", indent, v)
			}
			frame(depth+1, inner)
			fmt.Fprintf(&body, "%s}\n", indent)
			// The frame's own bindings again, after the inner with.
			for _, c := range held {
				fmt.Fprintf(&body, "%sConsole.Println(\"\\(C%d.Get())\")\n", indent, c)
				fmt.Fprintf(&out, "%d\n", bound[c])
			}
		}
	}
	// The outer context binds every capability, so each frame prints all.
	var every []int
	for i := 0; i < k; i++ {
		every = append(every, i)
	}
	outer, ob := literal(every)
	fmt.Fprintf(&body, "\twith %s {\n", outer)
	frame(2, ob)
	body.WriteString("\t}\n")
	b.WriteString("\nfunc main() needs Console {\n" + decls.String())
	for v := 0; v < nv; v++ {
		fmt.Fprintf(&b, "\t_ = v%d\n", v)
	}
	b.WriteString(body.String() + "}\n")
	return b.String(), out.String()
}

func ctxvalHas(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// TestContextValuesAgree is the seeded property (see above).
// It builds Go because the property is about which provider a call reaches
// when the program runs: the checker records the rows, and only the run
// shows that a value installs its own providers and no others.
func TestContextValuesAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4}
	if *ctxvalSeed != 0 {
		seeds = []uint64{*ctxvalSeed}
	}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	for _, seed := range seeds {
		src, want := ctxvalProgram(seed)
		programs = append(programs, mustLower(t, fmt.Sprintf("ctxval%d", seed), src))
		runs = append(runs, run{seed, src, want})
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the context-value programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[fmt.Sprintf("ctxval%d", r.seed)])
		if out != r.want || code != 0 {
			t.Errorf("seed %d: the program and the binding model differ\nReplay: go test -run '^TestContextValuesAgree$' ./internal/sketch/lower -args -ctxval.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, r.want)
		}
	}
}
