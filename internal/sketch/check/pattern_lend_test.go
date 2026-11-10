package check

import (
	"math/rand"
	"testing"
)

// checkPatternLends extends the release-graph model for a split aggregate
// and a binding lent from a temporary. Before PO1 these patterns were
// absent, so no old guard caught erasing/reversing their internal loans.
func checkPatternLends(t *testing.T, seed int, r *rand.Rand) {
	reverse, early := r.Intn(2) == 0, r.Intn(2) == 0
	pattern := "Pack{Root: p, Job: t}"
	if reverse {
		pattern = "Pack{Job: t, Root: p}"
	}
	body := "let " + pattern + " = Make()\n"
	if early {
		body += "p.Close()\n"
	} else {
		body += "t.Stop()\n"
	}
	body += "use(p)\n"
	src := `import task
type Fault {}
type R { N int }
func Close(r R) {}
func use(r R) {}
type Pack { Root R, Job Task[int, Fault] }
func Make() Pack { let p = R{N: 1}; Pack{Root: p, Job: task.Go(func() int fails Fault { use(p); 1 })} }
func Probe() {
` + body + "}\n"
	_, ds := checkFor(src)
	if (len(ds) > 0) != (reverse || early) {
		t.Fatalf("pattern loan seed %d; ERGO_SEED=%d replays\n%s\nchecker %v, wantBad %v", seed, seed, src, ds, reverse || early)
	}
	outside := r.Intn(2) == 0
	decl := "let s = tasks.NewSet[Fault](tasks.FailFast)\n"
	body = "match make().Root { case R as a:\n"
	if outside {
		body = decl + body
	} else {
		body += decl
	}
	body += "try tasks.Go(s) { use(a) }\n}\n"
	src = `import tasks
type Fault {}
type R { N int }
func Close(r R) {}
func use(r R) {}
type Box { Root R }
func make() Box { Box{Root: R{N: 1}} }
func Probe() fails tasks.Closed {
` + body + "}\n"
	_, ds = checkFor(src)
	if (len(ds) > 0) != outside {
		t.Fatalf("pattern temporary lend seed %d; ERGO_SEED=%d replays\n%s\nchecker %v, wantBad %v", seed, seed, src, ds, outside)
	}
	escape := r.Intn(2) == 0
	result, last := "", "f()"
	if escape {
		result, last = " func()", "return f"
	}
	src = `type R { N int }
func Close(r R) {}
type Box { Root R, Fn func() }
func Probe(b Box)` + result + ` { match b { case Box{Fn: f}: ` + last + " } }\n"
	_, ds = checkFor(src)
	if (len(ds) > 0) != escape {
		t.Fatalf("pattern callable lend seed %d; ERGO_SEED=%d replays\n%s\nchecker %v, wantBad %v", seed, seed, src, ds, escape)
	}
}
