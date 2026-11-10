package check

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestStructuredLendModel compares generated lifetime graphs with checker
// verdicts. It alone kills reversed keeper order, escaped incoming lends,
// lost container roots, and early takes while a keeper can run. The model
// orders release events, not source evaluation events. ERGO_SEED replays.
func TestStructuredLendModel(t *testing.T) {
	first, count := 1, 256
	if text := os.Getenv("ERGO_SEED"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil {
			t.Fatal(err)
		}
		first, count = n, 1
	}
	for seed := first; seed < first+count; seed++ {
		r := rand.New(rand.NewSource(int64(seed)))
		checkLendFields(t, seed, r)
		checkLendRoots(t, seed, r)
		incoming, container, nested := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
		set, earlier := r.Intn(2) == 0, r.Intn(2) == 0
		event := r.Intn(5) // lend, early release, drain/release, escape keeper, move keeper locally
		var src strings.Builder
		src.WriteString("import task\nimport tasks\ntype Fault {}\ntype R { N int }\nfunc Close(r R) {}\nfunc use(r R) {}\n")
		typ, value, use := "R", "R{N: 1}", "p"
		if container {
			typ, value, use = "[]R", "[]R{R{N: 1}}", "p[0]"
		}
		result := ""
		if event == 3 && !set {
			result = " Task[int, Fault]"
		}
		param := ""
		if incoming {
			param = "p " + typ
		}
		fmt.Fprintf(&src, "func Probe(%s)%s fails tasks.Closed {\n", param, result)
		setDecl := "let s = tasks.NewSet[Fault](tasks.FailFast)\n"
		if set && earlier {
			src.WriteString(setDecl)
		}
		if !incoming {
			fmt.Fprintf(&src, "let p = %s\n", value)
		}
		if nested {
			src.WriteString("with context {} {\n")
		}
		if set && !earlier {
			src.WriteString(setDecl)
		}
		if set {
			fmt.Fprintf(&src, "try tasks.Go(s) { use(%s) }\n", use)
		} else {
			fmt.Fprintf(&src, "let t = task.Go(func() int fails Fault { use(%s); 1 })\n", use)
		}
		// The reference release graph: local roots release at their block
		// end; incoming roots end at return. A Set declared earlier than a
		// local root releases after it, so this capture must move instead.
		lent := !set || incoming || !earlier
		wantBad := false
		if set && incoming && earlier {
			lent = true
		}
		keeper := "t"
		if set {
			keeper = "s"
		}
		switch event {
		case 0:
			fmt.Fprintf(&src, "use(%s)\n", use)
			wantBad = !lent
		case 1:
			if container {
				src.WriteString("drop(p)\n")
			} else {
				src.WriteString("p.Close()\n")
			}
			wantBad = true
		case 2:
			fmt.Fprintf(&src, "%s.Stop()\nuse(%s)\n", keeper, use)
			wantBad = !lent
		case 3:
			if !set {
				src.WriteString("return t\n")
				wantBad = true
			} else {
				fmt.Fprintf(&src, "use(%s)\n", use)
				wantBad = !lent
			}
		case 4:
			fmt.Fprintf(&src, "let later = %s\nuse(%s)\n", keeper, use)
			wantBad = !lent
		}
		if nested {
			src.WriteString("}\n")
		}
		src.WriteString("}\nfunc drop(p " + typ + ") takes p {}\n")
		text := src.String()
		if set {
			text = strings.ReplaceAll(text, "import task\n", "")
		}
		_, ds := checkFor(text)
		bad := len(ds) != 0
		if bad != wantBad {
			t.Fatalf("seed %d; replay ERGO_SEED=%d go test -p 2 -run '^TestStructuredLendModel$' ./internal/sketch/check\n%s\nchecker: %v\nmodel refusal: %t", seed, seed, src.String(), ds, wantBad)
		}
	}
}

func checkLendRoots(t *testing.T, seed int, r *rand.Rand) {
	drain, nestedClosure, escape := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
	use := "use(item)"
	if nestedClosure {
		use = "each(func() { use(item) })"
	}
	src := "import task\ntype Fault {}\ntype R { N int }\nfunc Close(r R) {}\nfunc use(r R) {}\nfunc each(f func()) { f() }\nfunc drop(xs []R) takes xs {}\nfunc Probe() { let xs = []R{R{N: 1}}\nfor item in xs {\nlet t = task.Go(func() int fails Fault { " + use + "; 1 })\n"
	if drain {
		src += "t.Stop()\n"
	}
	if escape {
		src += "drop(xs)\n"
	} else {
		src += "use(item)\n"
	}
	src += "break\n}\n}\n"
	_, ds := checkFor(src)
	wantBad := escape && !drain
	if (len(ds) != 0) != wantBad {
		t.Fatalf("root graph seed %d; replay ERGO_SEED=%d go test -p 2 -run '^TestStructuredLendModel$' ./internal/sketch/check\n%s\nchecker: %v\nmodel refusal: %t", seed, seed, src, ds, wantBad)
	}
}

// Generated record construction varies declaration order independently of
// evaluation order, and constructs the task in place or moves an existing
// task. The release-event model closes the task field before its root field.
func checkLendFields(t *testing.T, seed int, r *rand.Rand) {
	poolFirst, evalFirst, bound := r.Intn(2) == 0, r.Intn(2) == 0, r.Intn(2) == 0
	fields := "Pool R, Worker Task[int, Fault]"
	if !poolFirst {
		fields = "Worker Task[int, Fault], Pool R"
	}
	task := "task.Go(func() int fails Fault { use(p); 1 })"
	prefix := ""
	if bound {
		prefix, task = "let t = "+task+"\n", "t"
	}
	elts := "Pool: p, Worker: " + task
	if !evalFirst {
		elts = "Worker: " + task + ", Pool: p"
	}
	src := "import task\ntype Fault {}\ntype R { N int }\nfunc Close(r R) {}\nfunc use(r R) {}\ntype Pair { " + fields + " }\nfunc Make() Pair { let p = R{N: 1}\n" + prefix + "return Pair{" + elts + "}\n}\n"
	_, ds := checkFor(src)
	wantBad := !poolFirst || !bound && !evalFirst
	if (len(ds) != 0) != wantBad {
		t.Fatalf("field graph seed %d; replay ERGO_SEED=%d go test -p 2 -run '^TestStructuredLendModel$' ./internal/sketch/check\n%s\nchecker: %v\nmodel refusal: %t", seed, seed, src, ds, wantBad)
	}
}
