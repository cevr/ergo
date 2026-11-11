package check

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// ofPatternOwners extends the ownership model at its checking seam. Its
// reference is a partition of the resource leaves: disjoint bound parts
// own, whole/part overlaps and missing parts fail, and a lent root lends.
// The random shape and consumer are independent; ERGO_SEED replays both.
// Distinct mutants and the two original repros are in po1-plan.md and
// the check ledger (po1-*); the old grammar had no destructuring lets.
func ofPatternOwners(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0x706f31))
	pattern, value := "(a, b)", "(open(1), open(2))"
	switch r.IntN(3) {
	case 1:
		pattern, value = "Pair{R: a, S: b}", "pair()"
	case 2:
		pattern, value = "Nest{P: Pair{R: a, S: b}}", "Nest{P: pair()}"
	}
	body := "let " + pattern + " = " + value + "\nlend(b)\na.Close()\nlend(a)"
	wantBad := true
	mode := r.IntN(13)
	switch mode {
	case 1:
		body = "let " + pattern + " = " + value + "\nlend(a)\nlend(b)"
		wantBad = false
	case 2:
		body = "let Pair{R: a} = pair()\nlend(a)"
	case 3:
		body = "let Pair{R: a, S: b} as whole = pair()\nlend(a)\nlend(b)\nlendPair(whole)"
	case 4:
		body = "let Closed{R: a} = Closed{R: open(1)}\nlend(a)"
	case 5:
		body = "match p { case Pair{R: a, S: b}: lend(b); a.Close(); case Empty: }"
	case 6:
		body = "match makeHeld(n) { case Pair{R: a, S: b}: lend(a); lend(b); case Empty: }"
		wantBad = false
	case 7:
		body = "let a = match makeHeld(n) { case Pair{R: a, S: b}: lend(b); a; case Empty: open(3) }\nlend(a)"
		wantBad = false
	case 8:
		body = "match pair().R { case Res as a: keep(a) }"
	case 9:
		body = "match (open(1), open(2)) { case (a, b): lend(b); a.Close(); lend(a) }"
	case 10:
		body = "let Pair{R: _, S: b} = pair()\nlend(b)"
	case 11:
		body = "let result = plain[int]((1, 2))\nlet _ = result"
		wantBad = false
	case 12:
		body = "let Pair{R: a, S: b} = if n > 0 { keepHeld(p); pair() } else { pair() }\nlend(a)\nlend(b)"
	}
	src := ofPrelude + `type Pair { R Res, S Res }
type Nest { P Pair }
type Closed { R Res }
func Close(c Closed) {}
func pair() Pair { Pair{R: open(1), S: open(2)} }
func lendPair(p Pair) {}
type PatternHeld = Pair | Empty
func makeHeld(n int) PatternHeld { if n > 0 { return Empty }; pair() }
func plain[T](p (T, int)) int { let (_, n) = p; n }
func keepHeld(p PatternHeld) takes p {}
func Body(n int, p PatternHeld) {
` + body + "\n}\nfunc main() {}\n"
	c, ds := checkFor(src)
	bad := false
	for _, d := range ds {
		if d.Code != CodeType || (!strings.Contains(d.Message, "handed off") && !strings.Contains(d.Message, "pattern") && !strings.Contains(d.Message, "lent from") && !strings.Contains(d.Message, "lent to")) {
			t.Fatalf("pattern model seed %d: unexpected diagnostic %v\n%s", seed, d, src)
		}
		bad = true
	}
	if bad != wantBad {
		t.Fatalf("pattern model seed %d (ERGO_SEED=%d), mode %d: bad=%v want %v\n%s\n%v", seed, seed, mode, bad, wantBad, src, ds)
	}
	if wantBad {
		return
	}
	// Every resource binding of a consuming pattern must occur in the
	// release plan, unless its use transfers it to the arm's result.
	bound, planned := map[*Var]bool{}, map[*Var]bool{}
	syntax.Inspect(c.pkg.Scope.names["Body"].(*Func).Decl.Body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := c.pkg.Info.Defs[id].(*Var); ok && (id.Name == "a" || id.Name == "b") {
				bound[v] = true
			}
		}
		return true
	})
	for _, rs := range c.pkg.Info.Releases {
		for _, release := range rs {
			planned[release.Var] = true
		}
	}
	for v := range bound {
		if c.pkg.Info.OwnedVars[v] == nil || !planned[v] && mode != 7 {
			t.Fatalf("pattern model seed %d: %s has no owner/release (owned=%v planned=%v)\n%s", seed, v.Name, c.pkg.Info.OwnedVars[v] != nil, planned[v], src)
		}
	}
}

// ofPatternConsumers draws a pattern's shape, its cover and its consumer
// independently (16a fix r1, M3): one rule for every consuming pattern
// (resources R39), so a let, a match and a for-in over a queue's Values()
// (each element owned, Q7) refuse an omitted or wildcard resource part,
// and a for-in over a slice (lent, R35) admits it. Each binding of an
// admitted consuming pattern has an owner and a release. A queue's
// Values() Seq that escapes the range of a for-in is refused, in a
// generic body as a copy site (16a fix r2 and r3, M1; mutants
// 16a-r2-values-seq-kept, 16a-r3-generic-method-value,
// 16a-r3-values-copysite-dropped).
func ofPatternConsumers(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0x706f32))
	type shape struct{ typ, value, full, omit, wild string }
	shapes := []shape{
		{"(Res, Res)", "(open(1), open(2))", "(a, b)", "(_, b)", "(_, b)"},
		{"Pair", "pair()", "Pair{R: a, S: b}", "Pair{S: b}", "Pair{R: _, S: b}"},
		{"Nest", "Nest{P: pair()}", "Nest{P: Pair{R: a, S: b}}", "Nest{P: Pair{S: b}}", "Nest{P: Pair{R: _, S: b}}"},
	}
	s := shapes[r.IntN(len(shapes))]
	cover := r.IntN(3)
	pattern, uses := s.full, "lend(a)\nlend(b)"
	switch cover {
	case 1:
		pattern, uses = s.omit, "lend(b)"
	case 2:
		pattern, uses = s.wild, "lend(b)"
	}
	consumer := r.IntN(5)
	var body, helper string
	// An escape: a queue's Seq of owned values that reaches a for-in some
	// other way than as its range (16a fix r2 M1, fix r3 M1). Drawn from
	// every way a Seq or a function that makes one leaves the call: a let,
	// an argument, a method value (f = q.Values; f()), a record field, a
	// returned value and a closure's captured call. In a generic body over
	// Queue[T] (generic) each is a copy site of T (R44): instantiated with
	// the shape it is refused, with int it is admitted (plain).
	escape, generic, plain := r.IntN(6), consumer == 4 && r.IntN(2) == 1, false
	if consumer == 4 {
		e, p, u, tp := s.typ, pattern, uses, ""
		if generic {
			e, p, u, tp = "T", "v", "let _ = v", "[T]"
		}
		forIn := func(seq string) string { return "for " + p + " in " + seq + " {\n" + u + "\n}" }
		var esc string
		switch escape {
		case 0:
			esc = "let s = q.Values()\n" + forIn("s")
		case 1:
			// The callee's for-in lends, so its cover is not checked.
			helper = "func drain" + tp + "(s Seq[" + e + "]) {\n" + forIn("s") + "\n}\n"
			esc = "drain(q.Values())"
		case 2:
			esc = "let f = q.Values\n" + forIn("f()")
		case 3:
			helper = "type SeqBox" + tp + " { S Seq[" + e + "] }\n"
			esc = "let sb = SeqBox{S: q.Values()}\n" + forIn("sb.S")
		case 4:
			helper = "func vals" + tp + "(q queue.Queue[" + e + "]) Seq[" + e + "] { q.Values() }\n"
			esc = forIn("vals(q)")
		case 5:
			esc = "let g = func() Seq[" + e + "] { q.Values() }\n" + forIn("g()")
		}
		if generic {
			inst := s.typ
			if r.IntN(2) == 0 {
				inst, plain = "int", true
			}
			helper += "func h[T](q queue.Queue[T]) {\n" + esc + "\n}\n"
			body = "let q = queue.New[" + inst + "](1)\nh(q)"
		} else {
			body = "let q = queue.New[" + s.typ + "](1)\n" + esc
		}
	}
	switch consumer {
	case 0:
		body = "let " + pattern + " = " + s.value + "\n" + uses
	case 1:
		// An owned subject: the arm's pattern consumes it. (A literal
		// subject's parts are each a temporary of the statement, and its
		// pattern lends from them.)
		body = "let s = " + s.value + "\nmatch s { case " + pattern + ": " + strings.ReplaceAll(uses, "\n", "; ") + " }"
	case 2:
		body = "let q = queue.New[" + s.typ + "](1)\nfor " + pattern + " in q.Values() {\n" + uses + "\n}"
	case 3:
		body = "for " + pattern + " in xs {\n" + uses + "\n}"
	}
	// A Seq of a queue's owned values outside the range of a for-in has
	// no owner (resources R35): an escape is refused whatever the cover,
	// by that diagnostic alone (a method value by R19's), unless it is
	// generic and instantiated with plain data.
	seqOut := consumer == 4
	wantBad := seqOut && !plain || !seqOut && cover != 0 && consumer != 3
	src := ofPrelude + `type Pair { R Res, S Res }
type Nest { P Pair }
func pair() Pair { Pair{R: open(1), S: open(2)} }
` + helper + `func Body(n int, xs []` + s.typ + `) {
` + body + "\n}\nfunc main() {}\n"
	c, ds := checkFor(src)
	bad := false
	for _, d := range ds {
		want := "pattern"
		switch {
		case generic:
			want = "h cannot take a " + s.typ + ": h copies its T"
		case seqOut && escape == 2:
			want = "q.Values is a method value of Values on q, a resource"
		case seqOut:
			want = "q.Values() yields the queue's values"
		}
		if d.Code != CodeType || !strings.Contains(d.Message, want) {
			t.Fatalf("pattern consumer seed %d (ERGO_SEED=%d): unexpected diagnostic %v\n%s", seed, seed, d, src)
		}
		bad = true
	}
	if bad != wantBad {
		t.Fatalf("pattern consumer seed %d (ERGO_SEED=%d), cover %d consumer %d escape %d generic %v: bad=%v want %v\n%s\n%v", seed, seed, cover, consumer, escape, generic, bad, wantBad, src, ds)
	}
	if bad || consumer == 3 {
		return
	}
	planned := map[*Var]bool{}
	for _, rs := range c.pkg.Info.Releases {
		for _, release := range rs {
			planned[release.Var] = true
		}
	}
	syntax.Inspect(c.pkg.Scope.names["Body"].(*Func).Decl.Body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && (id.Name == "a" || id.Name == "b") {
			if v, ok := c.pkg.Info.Defs[id].(*Var); ok && (c.pkg.Info.OwnedVars[v] == nil || !planned[v]) {
				t.Fatalf("pattern consumer seed %d: %s has no owner/release (owned=%v planned=%v)\n%s", seed, v.Name, c.pkg.Info.OwnedVars[v] != nil, planned[v], src)
			}
		}
		return true
	})
}

// ofGenericRoutes crosses the route by which a queue of values reaches a
// generic body, the operation-type bound it meets there, the body's use
// of the queue, and the type argument, a resource or plain data (16a fix
// r4 and r5, M1 class): R22 decides every route at the instantiation
// (resources R22, R44). A route is a type parameter bound by nothing,
// reached directly, through a nested generic, as a method of a generic
// type and as a generic function value; an element type parameter bound
// by a drawn bound; the queue itself bound by a drawn bound, with the
// type arguments written or inferred, or through the bound of an outer
// generic's type parameter; a conversion to an operation type in the
// body (of an element, and of the queue, a sealed std protocol: C28);
// and a nested generic whose bound with Close binds an inner drawn bound.
// A bound is drawn from members (ofBound): a plain operation, one that
// takes a value, one that lends one, one that returns an owned value, a
// Values() Seq, and Close or none. A use is one of the six escapes of a
// queue's Values() Seq (ofPatternConsumers) or a lend that keeps
// nothing. The reference: a resource meets a bound only when the bound
// has a release (a Close; an operation that takes the value keeps it and
// releases nothing, R1/R13), every use keeps its release (a lend, or a
// bound with Close), and a sealed queue never converts. Mutants:
// 16a-r4-*, 16a-r5-*.
func ofGenericRoutes(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0x16a4))
	// The seed walks every route × use × argument bit: 400 seeds reach
	// each of the 168 at least twice; the shape and the bound are drawn.
	const routes = 12
	k := int(seed % (routes * 14))
	route, use, res := k/14, k/2%7, k%2 == 0
	shapes := []string{"(Res, Res)", "Pair", "Nest"}
	inst := shapes[r.IntN(len(shapes))]
	if !res {
		inst = "int"
	}
	escapes := use < 6
	tp, q, et, extra := "[T]", "queue.Queue[T]", "T", ""
	var elem, queueB ofBound
	switch route {
	case 4, 11:
		// An element bound; Close only for an argument with its own.
		// Route 11 converts such a value to the bound outside a generic.
		inst = "Num"
		if res {
			inst = []string{"Tagged", "TaggedC"}[r.IntN(2)]
		}
		elem = ofElemBound(r, inst == "TaggedC" && r.IntN(2) == 0)
		if route == 4 {
			tp = "[T B]"
		}
	case 5, 6, 10:
		queueB = ofQueueBound(r, escapes)
		tp, q = "[T any, Q QB[T]]", "Q"
		if route == 6 {
			tp, et = "[Q QB["+inst+"]]", inst
		}
	case 7:
		elem = ofElemBound(r, true)
		tp, inst, res = "[T B]", "TaggedC", true
	case 9:
		// A nested generic whose own type parameter, bound with Close,
		// binds the inner body's drawn bound: one without Close is the
		// outer body's copy site.
		elem = ofElemBound(r, !res)
		tp, inst, res = "[T B]", "TaggedC", true
	}
	var helper, esc string
	forIn := func(seq string) string { return "for v in " + seq + " {\nlet _ = v\n}" }
	switch use {
	case 0:
		esc = "let s = q.Values()\n" + forIn("s")
	case 1:
		helper = "func drain[T](s Seq[T]) {\n" + forIn("s") + "\n}\n"
		esc = "drain(q.Values())"
	case 2:
		esc = "let f = q.Values\n" + forIn("f()")
	case 3:
		helper = "type SeqBox[T] { S Seq[T] }\n"
		esc = "let sb = SeqBox{S: q.Values()}\n" + forIn("sb.S")
	case 4:
		helper = "func vals" + tp + "(q " + q + ") Seq[" + et + "] { q.Values() }\n"
		esc = forIn("vals(q)")
		if route == 5 || route == 10 {
			esc = forIn("vals[T, Q](q)")
		}
	case 5:
		esc = "let g = func() Seq[" + et + "] { q.Values() }\n" + forIn("g()")
	case 6:
		esc = "let _ = 0" // the queue is only lent
	}
	params, call := "q "+q, "h(q)"
	switch route {
	case 1:
		helper += "func outer[U](q queue.Queue[U]) {\nh(q)\n}\n"
		call = "outer(q)"
	case 2:
		helper += "type Run[T] { xs []T }\n"
		params, call = "r Run[T], q "+q, "Run["+inst+"]{xs: []"+inst+"{}}.h(q)"
	case 3:
		call = "let hv = h[" + inst + "]\nhv(q)"
	case 5:
		call = "h[" + inst + ", queue.Queue[" + inst + "]](q)"
	case 7:
		params, call = "q "+q+", x T", "h(q, TaggedC{R: open(3)})"
		extra = "let tg Tag = x\nlet _ = tg.Mark()\n"
	case 8:
		extra = "let src Src[T] = q\nlet _ = src\n"
	case 9:
		// outer's bound: the inner one's operations and Close.
		outerB := ofBound{members: elem.members}
		if !elem.close {
			outerB.members = append(slices.Clone(elem.members), "Close()")
		}
		helper += outerB.decl("BC") + "func outer[U BC](q queue.Queue[U]) {\nh(q)\n}\n"
		call = "outer(q)"
	case 10:
		// The bound reaches h through outer's own type parameter P.
		helper += "func outer[U any, P QB[U]](q P) {\nh[U, P](q)\n}\n"
		call = "outer[" + inst + ", queue.Queue[" + inst + "]](q)"
	case 11:
		lit := map[string]string{"Tagged": "Tagged{R: open(3)}", "TaggedC": "TaggedC{R: open(4)}", "Num": "Num{n: 1}"}[inst]
		call += "\nlet b B = " + lit + "\nlet _ = b.Mark()"
	}
	src := ofPrelude + `type Pair { R Res, S Res }
type Nest { P Pair }
type Tag { Mark() int }
type TagC { Mark() int, Close() }
type Src[E] { Values() Seq[E] }
type Tagged { R Res }
func Mark(t Tagged) int { 1 }
type TaggedC { R Res }
func Mark(t TaggedC) int { 2 }
func Close(t TaggedC) {}
type Num { n int }
func Mark(n Num) int { 3 }
` + elem.decl("B") + queueB.decl("QB[E]") + helper + "func h" + tp + "(" + params + ") {\n" + extra + esc + "\n}\nfunc Body() {\nlet q = queue.New[" + inst + "](1)\n" + call + "\n}\nfunc main() {}\n"
	// The reference.
	const (
		copies = "copies its"                 // R44: a copy site of the type parameter
		r22    = "has no Close, so the value" // R22 at the instantiation
		sealed = "is a sealed std protocol"   // C28
	)
	var wantBad bool
	want := []string{copies}
	switch route {
	case 0, 1, 2, 3:
		wantBad = res && escapes
	case 4:
		// A bound without a release refuses the resource (R22); one with
		// Close admits it, and the queue's escapes are the copy sites.
		wantBad = res && (!elem.close || escapes)
		if res && !elem.close {
			want = []string{r22, copies}
		}
	case 5, 6, 10:
		// A queue bound has no Close (the queue has none): never a release.
		wantBad, want = res, []string{r22, copies}
		if route == 6 {
			// The body names the element itself: a Seq boxed in a
			// record of it is R1/R13's own refusal too.
			want = append(want, "has no release")
		}
	case 7:
		wantBad = true
	case 8:
		wantBad, want = true, []string{sealed, copies}
	case 9:
		wantBad = escapes || !elem.close
	case 11:
		// The conversion: R22 refuses a resource into a bound without a
		// release, and a value of a bound that keeps what it takes and
		// has no Close is R1/R13's refusal, whatever converts to it.
		wantBad = res && (escapes || !elem.close) || elem.takes && !elem.close
		want = []string{copies, "has no release"}
		if res && !elem.close {
			want = []string{r22, copies, "has no release"}
		} else if !res {
			want = []string{"has no release"}
		}
	}
	_, ds := checkFor(src)
	bad, first := false, false
	for _, d := range ds {
		ok := false
		for i, w := range want {
			if strings.Contains(d.Message, w) {
				ok, first = true, first || i == 0
			}
		}
		if d.Code != CodeType || !ok {
			t.Fatalf("generic route seed %d (ERGO_SEED=%d), route %d use %d %s: unexpected diagnostic %v\n%s", seed, seed, route, use, inst, d, src)
		}
		bad = true
	}
	// The route's own rule reports: R22 for a resource under a bound
	// without Close, C28 for the queue's conversion.
	if bad != wantBad || bad && !first {
		t.Fatalf("generic route seed %d (ERGO_SEED=%d), route %d use %d %s: bad=%v want %v (first rule reported %v)\n%s\n%v", seed, seed, route, use, inst, bad, wantBad, first, src, ds)
	}
}

// ofBound is a drawn operation-type bound: its members, the functions
// that give Tagged, TaggedC and Num the element bound's operations, and
// whether it has Close (its one release, R1/R13).
type ofBound struct {
	members, funcs []string
	close, takes   bool
}

func (b ofBound) decl(name string) string {
	if len(b.members) == 0 {
		return ""
	}
	return "type " + name + " { " + strings.Join(b.members, ", ") + " }\n" + strings.Join(b.funcs, "")
}

// ofElemBound draws a bound of an element type parameter: Mark() always
// (route 7 converts to Tag), then each of an operation that takes a
// value, one that lends one, one that returns an owned value, and Close
// when close.
func ofElemBound(r *rand.Rand, close bool) ofBound {
	b := ofBound{members: []string{"Mark() int"}, close: close}
	for _, m := range []struct{ decl, fn string }{
		{"Keep(o Res) takes o", "func Keep(t %s, o Res) takes o { keep(o) }\n"},
		{"See(o Res) bool", "func See(t %s, o Res) bool { held(o) }\n"},
		{"Make() Res", "func Make(t %s) Res { open(1) }\n"},
	} {
		if r.IntN(2) == 0 {
			b.takes = b.takes || strings.Contains(m.decl, "takes")
			b.members = append(b.members, m.decl)
			for _, typ := range []string{"Tagged", "TaggedC", "Num"} {
				b.funcs = append(b.funcs, strings.ReplaceAll(m.fn, "%s", typ))
			}
		}
	}
	if close {
		b.members = append(b.members, "Close()")
	}
	return b
}

// ofQueueBound draws a bound over E that queue.Queue[E] satisfies: Values
// when the body reads it, and each of Push (takes the value), Pop and
// Poll (return an owned one), Len and End (plain). The queue has no
// Close, so no queue bound has a release.
func ofQueueBound(r *rand.Rand, values bool) ofBound {
	var b ofBound
	if values || r.IntN(2) == 0 {
		b.members = append(b.members, "Values() Seq[E]")
	}
	for _, m := range []string{"Push(v E) fails queue.Closed takes v", "Pop() E fails queue.Closed", "Poll() Option[E]", "Len() int", "End()"} {
		if r.IntN(2) == 0 {
			b.members = append(b.members, m)
		}
	}
	if len(b.members) == 0 {
		b.members = []string{"Len() int"}
	}
	return b
}
