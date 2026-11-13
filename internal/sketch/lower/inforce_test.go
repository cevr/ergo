package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
)

// The held contexts' seeded property (rows.md, Context values, InForce;
// CK-rowarg part 3, Q16). Each seed writes a program over k capabilities
// C0…Ck-1, each `Get() int`, which one provider Box implements. main binds
// every capability in an outer with, then runs steps: a hold, which keeps
// a function that reads a random set S of the capabilities with
// InForce[R]() in a record, inside a with that rebinds some of them; and a
// call of a kept record, inside a with that rebinds others, through a
// generic runner `call[R](h Held[R])` whose with installs the held
// context, or `callPlus`, which also reads a capability Cx at the call.
// A hold is made four ways: the generic hold, a generic caller that passes
// its own row on (holdVia), one that adds C0 to it (holdMore, a join of a
// row parameter and a capability), and InForce of a written row in a
// function with no row parameter (holdC). The generator is the model: a
// held function sees the boxes in force at its hold for S, and the boxes
// at the call for every other capability. A with that installed the call
// site's providers, a copy of only some of R, or a copy of more than R
// prints other numbers.
//
// Replay: go test -run '^TestInForceContextsAgree$' ./internal/sketch/lower -args -inforce.seed=N

var inforceSeed = flag.Uint64("inforce.seed", 0, "run TestInForceContextsAgree on this seed alone")

// inforceProgram writes the program of seed, the output its model
// predicts, and the forms it reached.
func inforceProgram(seed uint64) (string, string, map[string]bool) {
	r := rand.New(rand.NewPCG(seed, 0x1f0c))
	k := 2 + r.IntN(3)
	reached := map[string]bool{}
	var b, body, out strings.Builder
	b.WriteString("type Box { n int }\n")
	for i := range k {
		fmt.Fprintf(&b, "\n// law-exempt: the property's operation types.\ntype C%d {\n\tGet() int,\n}\n", i)
	}
	b.WriteString("\nfunc Get(b Box) int { b.n }\n")
	b.WriteString("\ntype Held[R] { Run func() string needs R, Ctx Context[R] }\n")
	b.WriteString("\nfunc hold[R](f escaping func() string needs R) Held[R] needs R {\n\tHeld[R]{Run: f, Ctx: InForce[R]()}\n}\n")
	b.WriteString("\nfunc holdVia[R](f escaping func() string needs R) Held[R] needs R { hold(f) }\n")
	b.WriteString("\nfunc holdMore[R](f escaping func() string needs R | C0) Held[R | C0] needs R | C0 { hold(f) }\n")
	b.WriteString("\nfunc holdC() Held[C0 | C1] needs C0 | C1 {\n\tHeld[C0 | C1]{Run: func() string { \"\\(C0.Get()) \\(C1.Get())\" }, Ctx: InForce[C0 | C1]()}\n}\n")
	b.WriteString("\nfunc call[R](h Held[R]) string {\n\twith h.Ctx { h.Run() }\n}\n")
	for x := range k {
		fmt.Fprintf(&b, "\nfunc callPlus%d[R](h Held[R]) string needs C%d {\n\twith h.Ctx { h.Run() + \" | \\(C%d.Get())\" }\n}\n", x, x, x)
	}
	next := 100
	box := func() int { next++; return next }
	// literal binds a random nonempty subset of the capabilities to fresh
	// boxes and returns the context and the bindings over bound.
	literal := func(bound map[int]int, every bool) (string, map[int]int) {
		inner := map[int]int{}
		for c, n := range bound {
			inner[c] = n
		}
		var entries []string
		for c := range k {
			if every || r.IntN(2) == 0 || len(entries) == 0 && c == k-1 {
				n := box()
				inner[c] = n
				entries = append(entries, fmt.Sprintf("C%d: Box{n: %d}", c, n))
			}
		}
		return "context { " + strings.Join(entries, ", ") + " }", inner
	}
	outer := map[int]int{}
	var all []string
	for c := range k {
		n := box()
		outer[c] = n
		all = append(all, fmt.Sprintf("C%d: Box{n: %d}", c, n))
	}
	// A kept record: the boxes its function reads, in the order it prints.
	type kept struct {
		caps  []int
		boxes map[int]int
	}
	var holds []kept
	steps := 6 + r.IntN(5)
	for s := 0; s < steps; s++ {
		if len(holds) == 0 || r.IntN(3) == 0 {
			lit, at := literal(outer, false)
			var caps []int
			form := r.IntN(4)
			if form == 3 {
				caps = []int{0, 1}
			} else {
				for c := range k {
					if r.IntN(2) == 0 {
						caps = append(caps, c)
					}
				}
				if form == 2 && !ctxvalHas(caps, 0) {
					caps = append([]int{0}, caps...)
					sort.Ints(caps)
				}
				if len(caps) == 0 {
					caps = []int{r.IntN(k)}
				}
			}
			var reads []string
			for _, c := range caps {
				reads = append(reads, fmt.Sprintf("\\(C%d.Get())", c))
			}
			fn := "{ \"" + strings.Join(reads, " ") + "\" }"
			var made string
			switch form {
			case 0:
				made = "hold() " + fn
			case 1:
				made = "holdVia() " + fn
			case 2:
				made = "holdMore() " + fn
			default:
				made = "holdC()"
			}
			reached[fmt.Sprintf("form %d", form)] = true
			fmt.Fprintf(&body, "\tlet h%d = with %s { %s }\n", len(holds), lit, made)
			holds = append(holds, kept{caps: caps, boxes: at})
			continue
		}
		i := r.IntN(len(holds))
		h := holds[i]
		// A call rebinds every capability half the time, so a held value
		// and the call site's differ.
		lit, at := literal(outer, r.IntN(2) == 0)
		var want []string
		for j, c := range h.caps {
			want = append(want, fmt.Sprint(h.boxes[c]))
			if h.boxes[c] != at[c] {
				reached["shadowed"] = true
				if j > 0 {
					reached["shadowed past the first"] = true
				}
			}
		}
		line := strings.Join(want, " ")
		runner := "call"
		if r.IntN(2) == 0 {
			x := r.IntN(k)
			runner = fmt.Sprintf("callPlus%d", x)
			n := at[x]
			if ctxvalHas(h.caps, x) {
				n = h.boxes[x]
				reached["plus held"] = true
			} else if n != h.boxes[x] {
				reached["plus call site"] = true
			}
			line += fmt.Sprintf(" | %d", n)
		}
		fmt.Fprintf(&body, "\twith %s { Console.Println(%s(h%d)) }\n", lit, runner, i)
		fmt.Fprintf(&out, "%s\n", line)
	}
	b.WriteString("\nfunc main() needs Console {\n\twith context { " + strings.Join(all, ", ") + " } {\n")
	for i := range holds {
		fmt.Fprintf(&body, "\t_ = h%d\n", i)
	}
	b.WriteString(body.String() + "\t}\n}\n")
	return b.String(), out.String(), reached
}

// TestInForceContextsAgree is the seeded property (see above).
// It builds Go because the property is about which provider a held
// function reaches when the program runs: the checker records the rows,
// and only the run shows that the held context installs the providers of
// its hold, all of R and nothing else.
func TestInForceContextsAgree(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *inforceSeed != 0 {
		seeds = []uint64{*inforceSeed}
	}
	type run struct {
		seed      uint64
		src, want string
	}
	var runs []run
	var programs []*lowered
	reached := map[string]bool{}
	for _, seed := range seeds {
		src, want, re := inforceProgram(seed)
		for k := range re {
			reached[k] = true
		}
		programs = append(programs, mustLower(t, fmt.Sprintf("inforce%d", seed), src))
		runs = append(runs, run{seed, src, want})
	}
	if *inforceSeed == 0 {
		for _, k := range []string{"form 0", "form 1", "form 2", "form 3", "shadowed", "shadowed past the first", "plus held", "plus call site"} {
			if !reached[k] {
				t.Errorf("the seeds never reach %s (reached %v)", k, reached)
			}
		}
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		t.Fatalf("the held-context programs do not build (seeds %v)", seeds)
	}
	for _, r := range runs {
		out, stderr, code := runExe(t, exes[fmt.Sprintf("inforce%d", r.seed)])
		if out != r.want || code != 0 {
			t.Errorf("seed %d: the program and the binding model differ\nReplay: go test -run '^TestInForceContextsAgree$' ./internal/sketch/lower -args -inforce.seed=%d\n%s\n--- program (exit %d)\n%s%s\n--- model\n%s", r.seed, r.seed, r.src, code, out, stderr, r.want)
		}
	}
}

// TestInForceAfterWithEnds is the defect of a held context installed after
// the owning with in force at its hold has ended (rows.md, Context values,
// InForce): Conn has a Close, so the with owns it and closes it at its
// end, and the second call would read a closed provider. The first call,
// inside the with, runs.
//
// It builds Go because the defect is raised when the program runs; the
// checker cannot see a held value outlive its with (Ergo has no
// lifetimes).
func TestInForceAfterWithEnds(t *testing.T) {
	needsBuild(t)
	o := mustLower(t, "inforcedefect", `type Store { Get() string }

type Conn { n int }

func Get(c Conn) string { "conn \(c.n)" }

func Close(c Conn) needs Console { Console.Println("closed \(c.n)") }

func open(n int) Conn { Conn{n: n} }

type Held[R] { Run func() string needs R, Ctx Context[R] }

func hold[R](f escaping func() string needs R) Held[R] needs R {
	Held[R]{Run: f, Ctx: InForce[R]()}
}

func call[R](h Held[R]) string {
	with h.Ctx { h.Run() }
}

func main() needs Console {
	let h = with context { Store: open(1) } {
		Console.Println(call(hold() { Store.Get() }))
		hold() { Store.Get() }
	}
	Console.Println(call(h))
}
`)
	exes := buildAll(t, []*lowered{o}, true)
	if exes == nil {
		t.Fatal("the program does not build")
	}
	out, stderr, code := runExe(t, exes[o.name])
	if out != "conn 1\nclosed 1\n" || code == 0 || !strings.Contains(stderr, "a held context was used after its with ended") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, firstLines(stderr, 8))
	}
}
