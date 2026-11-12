package check

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// The borrow-conversion rule's seeded property (types.md, Borrows 6;
// borrowconv.go). Each seed writes functions that each take a value from a
// source (a borrow parameter t, a borrow &x of a let mut, or an owned
// Fixed), convert it to the operation type Counter at a site (a let, a
// with's context, a context value, a return, an argument, a field, an
// element, an Option or union payload, a generic instantiation, an
// assignment, an if arm, a tuple a multi-name let takes apart), and, after a let, pass the local through zero to
// two more lets and then one escape route (an operation call, the same
// sites, a capture by an escaping or a non-escaping literal, by task.Go).
// A fourth of the functions take a borrow b *strings.Builder to the Go
// interface io.Writer instead (bcGoCase), where a reviewed lent argument
// of a Go call admits the borrow itself and a kept one refuses it by the
// Go argument rule (ownership.go). A fifth instantiate a type parameter
// with the value itself (bcGenericCase): a borrow is a type argument only
// of a body that keeps no value of it (Borrows 7).
// The lifetime model: a value from an owned source goes anywhere; a value
// from a borrow stands only as a one-name let's value, a with's context
// entry, or (as a local) an operation call's receiver or a capture by a
// frame that cannot outlive the call; every other place is refused at the
// value's line, naming the borrow or the local that holds it. The
// checker's Borrows 6 lines and names must be the model's, and no other
// diagnostic may appear.
//
// Replay: go test -run '^TestBorrowConvModel$' ./internal/sketch/check -args -borrowconv.seed=N

var borrowConvSeed = flag.Uint64("borrowconv.seed", 0, "run TestBorrowConvModel on this seed alone")

const borrowConvPrelude = `import task
import go bufio
import go io
import go strings

// law-exempt: the property's operation type.
type Counter {
	Bump() int,
}

type Tally { n int }

func Bump(t *Tally) int {
	t.n = t.n + 1
	t.n
}

type Fixed {}

func Bump(f Fixed) int { 1 }

type Holder { c Counter }

type Wrap = Has{c Counter} | Empty{}

func id[T](x T) T { x }

func use(c Counter) int { c.Bump() }

func run(f escaping func() int) int { f() }

func each(f func() int) int { f() }

type Box[T] { V T }

func hold[T](x T, use escaping func(v T) int) func() int { func() int { use(x) } }

func later[T](x T) func() T { func() T { x } }

func viaOp[T lent Counter](x T) int { x.Bump() }

func opPlain[T Counter](x T) int { x.Bump() }

func fwd[T](x T) T { id(x) }

func fwdOp[T lent Counter](x T) int { viaOp(x) }

func ret[T](x T) T { return x }

func arm[T](x T) T { if len("a") > 0 { x } else { x } }

func local[T lent](x T) int {
	let y = x
	let _ = y
	1
}

func via[T](x T, use escaping func(v T) int) func() int { hold(x, use) }

func spawn[T](x T, use escaping func(v T) int) int { task.Go() { use(x) }.Wait() }

func apply[T lent](x T, use func(v T) int) int { use(x) }

func fwdLocal[T lent](x T) int { local(x) }

func put[T lent](out *T, x T) { *out = x }

func relay[T lent](x T, use escaping func(v T) int) int { use(x) + run(func() int { let _ = use; 1 }) }

func waited() int { task.Go() { 1 }.Wait() }

func goUses() int { bufio.MaxScanTokenSize + io.SeekEnd + len(strings.Repeat("", 0)) }
`

// A bcSite is a place a value stands: the function's result type, the
// statements before and after the value, and the line that holds it (%s).
type bcSite struct {
	name          string
	result        string
	before, after []string
	at            string
	// ok: a borrow may stand here.
	ok bool
}

// bcSites are the places a converted value or its local may stand. A
// site's "%s" is the value.
var bcSites = []bcSite{
	{name: "with", result: "int", at: "with context { Counter: %s } { Counter.Bump() }", ok: true},
	{name: "ctxval", result: "Context[Counter]", at: "let w Context[Counter] = context { Counter: %s }", after: []string{"w"}, ok: false},
	{name: "tail", result: "Counter", at: "%s", ok: false},
	{name: "return", result: "Counter", at: "return %s", ok: false},
	{name: "arg", result: "int", at: "use(%s)", ok: false},
	{name: "field", result: "Holder", at: "Holder{c: %s}", ok: false},
	{name: "elem", result: "[]Counter", at: "[]Counter{%s}", ok: false},
	{name: "option", result: "Option[Counter]", at: "Some[Counter]{Value: %s}", ok: false},
	{name: "union", result: "Wrap", at: "Has{c: %s}", ok: false},
	{name: "generic", result: "Counter", at: "id[Counter](%s)", ok: false},
	{name: "assign", result: "int", before: []string{"let mut m Counter = Fixed"}, at: "m = %s", after: []string{"m.Bump()"}, ok: false},
	{name: "pair", result: "int", at: "let a, b Counter = (%s, Fixed)", after: []string{"a.Bump() + b.Bump()"}, ok: false},
	{name: "arm", result: "Counter", at: "if len(\"a\") > 0 { %s } else { Fixed }", ok: false},
}

// bcLocalSites are the places only a local may stand.
var bcLocalSites = []bcSite{
	{name: "call", result: "int", at: "%s.Bump()", ok: true},
	{name: "inferred", result: "Counter", at: "id(%s)", ok: false},
	{name: "local-capture", result: "int", at: "each(func() int { %s.Bump() })", ok: true},
	{name: "escaping-capture", result: "int", at: "run(func() int { %s.Bump() })", ok: false},
	{name: "task", result: "int", at: "task.Go() { %s.Bump() }.Wait()", ok: false},
}

// bcGoSites are the places a borrow of a Go value converted to a Go
// interface may stand: a reviewed lent argument of a Go call (the Go
// argument rule admits it), a kept one (that rule refuses it: "kept"), a
// tail. bcGoLocalSites are the places only its local may stand: an
// operation call. A local that holds the borrow is no Go argument at all.
var bcGoSites = []bcSite{
	{name: "go-lent", result: "int", at: "let n = io.WriteString(%s, \"x\") catch {", after: []string{"case go.Error:", "\treturn 0", "}", "n"}, ok: true},
	{name: "go-kept", result: "int", at: "bufio.NewWriter(%s)", after: []string{"0"}, ok: false},
	{name: "go-tail", result: "io.Writer", at: "%s", ok: false},
}

var bcGoLocalSites = []bcSite{
	{name: "go-call", result: "int", at: "let n = %s.Write([]byte{120}) catch {", after: []string{"case go.Error:", "\treturn 0", "}", "n"}, ok: true},
}

// bcGoCase is bcCase over a borrow b *strings.Builder converted to
// io.Writer.
func bcGoCase(r *rand.Rand, name string) ([]string, int, string, string) {
	lines := []string{""}
	body := func(s string) int {
		lines = append(lines, "\t"+s)
		return len(lines) - 1
	}
	finish := func(site bcSite, value string) int {
		lines[0] = fmt.Sprintf("func %s(b *strings.Builder) %s needs go.Foreign {", name, site.result)
		at := body(fmt.Sprintf(site.at, value))
		for _, s := range site.after {
			body(s)
		}
		lines = append(lines, "}")
		return at
	}
	if r.IntN(3) == 0 {
		site := bcGoSites[r.IntN(len(bcGoSites))]
		at, named := finish(site, "b"), "b"
		if site.ok {
			at = 0
		}
		if site.name == "go-kept" {
			named = "kept"
		}
		return lines, at, named, "go direct b " + site.name
	}
	body("let w io.Writer = b")
	local := "w"
	for k := range r.IntN(3) {
		next := fmt.Sprintf("d%d", k)
		body(fmt.Sprintf("let %s = %s", next, local))
		local = next
	}
	all := append(append([]bcSite{}, bcGoSites...), bcGoLocalSites...)
	site := all[r.IntN(len(all))]
	at := finish(site, local)
	if site.name == "go-call" {
		at = 0
	}
	return lines, at, local, "go local b " + site.name
}

// bcGenericSites are the places a value is a type argument. A borrow
// instantiates only a type parameter whose signature writes lent (q137,
// EX3; the call reads no body), and the body of each lent one keeps no
// value of it: it binds it to its own locals (local), passes it on to
// such a body (fwdLocal), passes it to a function value's parameter
// (apply), writes it through a borrow parameter into the caller's local
// (put), and keeps only a function value (relay); a method-constrained T
// writes lent before its constraint, [T lent Counter], and calls an
// operation on it (viaOp) or passes it on (fwdOp). opPlain, whose body
// keeps none but whose signature does not write lent, refuses a borrow,
// as every unmarked T does. One that keeps it
// refuses a borrow: it returns it, as its tail (id), by return (ret), as
// an if's value (arm) or through a body that does (fwd; INS-3); it
// captures it in an escaping closure (hold, later),
// passes it to a body that does (via), or captures it in task.Go's block
// (spawn); and a generic record literal never holds one (Box). A function
// type that takes a borrow is a type argument even of a body that keeps it
// (targ-func). %[1]s is the
// value and %[2]s its type.
var bcGenericSites = []bcSite{
	{name: "targ-id", result: "int", at: "let u = id(%[1]s)", after: []string{"u.Bump()"}},
	{name: "targ-ret", result: "int", at: "let u = ret(%[1]s)", after: []string{"u.Bump()"}},
	{name: "targ-arm", result: "int", at: "let u = arm(%[1]s)", after: []string{"u.Bump()"}},
	{name: "targ-fwd", result: "int", at: "let u = fwd(%[1]s)", after: []string{"u.Bump()"}},
	{name: "targ-local", result: "int", at: "local(%[1]s)", ok: true},
	{name: "targ-op", result: "int", at: "viaOp(%[1]s)", ok: true},
	{name: "targ-fwdop", result: "int", at: "fwdOp(%[1]s)", ok: true},
	{name: "targ-opplain", result: "int", at: "opPlain(%[1]s)"},
	{name: "targ-fwdlocal", result: "int", at: "fwdLocal(%[1]s)", ok: true},
	{name: "targ-put", result: "int", before: []string{"let mut q = %[1]s"}, at: "put(&q, %[1]s)", after: []string{"q.Bump()"}, ok: true},
	{name: "targ-apply", result: "int", at: "apply(%[1]s, func(v %[2]s) int { v.Bump() })", ok: true},
	{name: "targ-relay", result: "int", at: "relay(%[1]s, func(v %[2]s) int { v.Bump() })", ok: true},
	{name: "targ-later", result: "int", at: "let g = later(%[1]s)", after: []string{"g().Bump()"}},
	{name: "targ-via", result: "int", at: "let f = via(%[1]s, func(v %[2]s) int { v.Bump() })", after: []string{"f()"}},
	{name: "targ-task", result: "int", at: "spawn(%[1]s, func(v %[2]s) int { v.Bump() })"},
	{name: "targ-hold", result: "int", at: "let f = hold(%[1]s, func(v %[2]s) int { v.Bump() })", after: []string{"f()"}},
	{name: "targ-box", result: "int", at: "let b = Box{V: %[1]s}", after: []string{"b.V.Bump()"}},
	{name: "targ-func", result: "int", at: "let g = later(func(v %[2]s) int { v.Bump() })", after: []string{"g()(%[1]s)"}, ok: true},
}

// bcGenericCase is bcCase over a value that instantiates a type
// parameter: a borrow (t, &x) is refused at its line, naming the generic
// function or the record type; an owned Fixed is accepted.
func bcGenericCase(r *rand.Rand, name string) ([]string, int, string, string) {
	sources := []string{"t", "&x", "Fixed"}
	source := sources[r.IntN(len(sources))]
	typ := map[string]string{"t": "*Tally", "&x": "*Tally", "Fixed": "Fixed"}[source]
	site := bcGenericSites[r.IntN(len(bcGenericSites))]
	lines := []string{fmt.Sprintf("func %s(t *Tally) %s {", name, site.result)}
	body := func(s string) int {
		lines = append(lines, "\t"+s)
		return len(lines) - 1
	}
	if source == "&x" {
		body("let mut x = Tally{n: 0}")
	}
	for _, a := range site.before {
		body(fmt.Sprintf(a, source, typ))
	}
	at := body(fmt.Sprintf(site.at, source, typ))
	for _, a := range site.after {
		if strings.Contains(a, "%") {
			a = fmt.Sprintf(a, source, typ)
		}
		body(a)
	}
	lines = append(lines, "}")
	named := map[string]string{"targ-id": "typearg id", "targ-ret": "typearg ret", "targ-arm": "typearg arm", "targ-fwd": "typearg fwd", "targ-hold": "typearg hold", "targ-box": "typearg Box[*Tally]", "targ-later": "typearg later", "targ-via": "typearg via", "targ-task": "typearg spawn", "targ-opplain": "typearg opPlain"}[site.name]
	if source == "Fixed" || site.ok {
		at = 0
	}
	return lines, at, named, "generic " + source + " " + site.name
}

// bcCase writes one function and returns its lines, the model's refused
// line (0 for none), counted from the function's first line, the value the
// refusal names, and the case's name.
func bcCase(r *rand.Rand, name string) ([]string, int, string, string) {
	switch r.IntN(5) {
	case 0:
		return bcGoCase(r, name)
	case 1:
		return bcGenericCase(r, name)
	}
	sources := []string{"t", "&x", "Fixed"}
	source := sources[r.IntN(len(sources))]
	borrow := source != "Fixed"
	lines := []string{""}
	body := func(s string) int {
		lines = append(lines, "\t"+s)
		return len(lines) - 1
	}
	finish := func(site bcSite, value string) (int, string) {
		lines[0] = fmt.Sprintf("func %s(t *Tally) %s {", name, site.result)
		for _, s := range site.before {
			body(s)
		}
		at := body(fmt.Sprintf(site.at, value))
		for _, s := range site.after {
			body(s)
		}
		lines = append(lines, "}")
		return at, site.name
	}
	if source == "&x" {
		body("let mut x = Tally{n: 0}")
	}
	if r.IntN(3) == 0 {
		// The value converts at the site itself.
		site := bcSites[r.IntN(len(bcSites))]
		at, what := finish(site, source)
		if !borrow || site.ok {
			at = 0
		}
		return lines, at, source, "direct " + source + " " + what
	}
	body(fmt.Sprintf("let c Counter = %s", source))
	local := "c"
	for k := range r.IntN(3) {
		next := fmt.Sprintf("d%d", k)
		body(fmt.Sprintf("let %s = %s", next, local))
		local = next
	}
	all := append(append([]bcSite{}, bcSites...), bcLocalSites...)
	site := all[r.IntN(len(all))]
	at, what := finish(site, local)
	if !borrow || site.ok {
		at = 0
	}
	return lines, at, local, "local " + source + " " + what
}

// borrowConvProgram is seed's source, the model's refused lines, and the
// names of the cases it reaches.
func borrowConvProgram(seed uint64) (string, map[int]string, []string) {
	r := rand.New(rand.NewPCG(seed, 0xb0c0))
	var b strings.Builder
	b.WriteString(borrowConvPrelude)
	line := strings.Count(borrowConvPrelude, "\n")
	want := map[int]string{}
	var reached []string
	for i := range 2 + r.IntN(4) {
		lines, at, named, what := bcCase(r, fmt.Sprintf("f%d", i))
		b.WriteString("\n")
		line++
		if at != 0 {
			want[line+1+at] = named
		}
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		line += len(lines)
		ok := "accepted"
		if at != 0 {
			ok = "refused"
		}
		reached = append(reached, what+" "+ok)
	}
	b.WriteString("\nfunc main() {}\n")
	return b.String(), want, reached
}

// TestBorrowConvModel is the property (see above).
func TestBorrowConvModel(t *testing.T) {
	seeds := []uint64{}
	for s := range uint64(600) {
		seeds = append(seeds, s+1)
	}
	if *borrowConvSeed != 0 {
		seeds = []uint64{*borrowConvSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src, want, cases := borrowConvProgram(seed)
		_, diags := CheckSource(src)
		got := map[int]string{}
		for _, d := range diags {
			// A refusal names the borrow or the local that holds it.
			named, _, ok := strings.Cut(d.Message, " is a borrow (*T)")
			if !ok {
				named, _, ok = strings.Cut(d.Message, " holds a borrow (*T)")
			}
			switch {
			case d.Code == "S108" && ok && strings.Contains(d.Message, "Borrows 6"):
				got[d.Line] = named
			case d.Code == "S108" && strings.Contains(d.Message, "may keep it; pass an owned Go reference"):
				got[d.Line] = "kept"
			case d.Code == "S108" && strings.Contains(d.Message, " cannot take a borrow as its ") && strings.Contains(d.Message, "Borrows 7"):
				f, _, _ := strings.Cut(d.Message, " ")
				got[d.Line] = "typearg " + f
			case d.Code == "S108" && strings.HasPrefix(d.Message, "this value of ") && strings.Contains(d.Message, "Borrows 7"):
				t, _, _ := strings.Cut(strings.TrimPrefix(d.Message, "this value of "), " ")
				got[d.Line] = "typearg " + t
			default:
				got[d.Line] = d.String()
			}
		}
		if !mapsEqual(got, want) {
			t.Errorf("seed %d: the checker says %v, the model %v\nReplay: go test -run '^TestBorrowConvModel$' ./internal/sketch/check -args -borrowconv.seed=%d\n%s", seed, got, want, seed, src)
		}
		for _, c := range cases {
			reached[c] = true
		}
	}
	if *borrowConvSeed != 0 {
		return
	}
	// Every site and route is reached from a borrow and from the owned
	// source.
	for _, s := range bcSites {
		for _, src := range []string{"t", "&x", "Fixed"} {
			for _, form := range []string{"direct", "local"} {
				ok := "refused"
				if src == "Fixed" || s.ok {
					ok = "accepted"
				}
				if k := form + " " + src + " " + s.name + " " + ok; !reached[k] {
					t.Errorf("no seed reaches %q", k)
				}
			}
		}
	}
	for _, s := range bcGoSites {
		ok := "refused"
		if s.ok {
			ok = "accepted"
		}
		for _, k := range []string{"go direct b " + s.name + " " + ok, "go local b " + s.name + " refused"} {
			if !reached[k] {
				t.Errorf("no seed reaches %q", k)
			}
		}
	}
	for _, s := range bcGenericSites {
		for _, src := range []string{"t", "&x", "Fixed"} {
			if k := "generic " + src + " " + s.name + map[bool]string{true: " accepted", false: " refused"}[src == "Fixed" || s.ok]; !reached[k] {
				t.Errorf("no seed reaches %q", k)
			}
		}
	}
	if !reached["go local b go-call accepted"] {
		t.Errorf("no seed reaches %q", "go local b go-call accepted")
	}
	for _, s := range bcLocalSites {
		for _, src := range []string{"t", "&x", "Fixed"} {
			ok := "refused"
			if src == "Fixed" || s.ok {
				ok = "accepted"
			}
			if k := "local " + src + " " + s.name + " " + ok; !reached[k] {
				t.Errorf("no seed reaches %q", k)
			}
		}
	}
}

// TestBorrowConvRepro is the smallest repro of the fixed bug (PS3
// follow-up): a borrow converted to an operation type by a let was
// returned, and the program built and ran. It pins the diagnostic's text
// and position.
func TestBorrowConvRepro(t *testing.T) {
	src := `// law-exempt: the repro's operation type.
type Counter {
	Bump() int,
}

type Tally { n int }

func Bump(t *Tally) int { t.n }

func keep(t *Tally) Counter {
	let c Counter = t
	c
}
`
	_, diags := CheckSource(src)
	want := "12:2: S108 c holds a borrow (*T) as Counter, which lives only as long as the call that lent it: call its operations or bind it with a let, never return, store or pass it or convert it again (types.md, Borrows 6)"
	if len(diags) != 1 || diags[0].String() != want {
		t.Fatalf("got %v, want [%s]", diags, want)
	}
}

// bcLentSites names, for each generic function of the prelude, the
// bcGenericSites case whose model says whether its body keeps a value of
// T (the case's ok is false).
var bcLentSites = map[string]string{
	"id": "targ-id", "ret": "targ-ret", "arm": "targ-arm", "fwd": "targ-fwd",
	"later": "targ-later", "via": "targ-via", "spawn": "targ-task", "hold": "targ-hold",
	"local": "targ-local", "fwdLocal": "targ-fwdlocal", "put": "targ-put",
	"apply": "targ-apply", "relay": "targ-relay",
	"viaOp": "targ-op", "fwdOp": "targ-fwdop", "opPlain": "targ-op",
}

// TestLentMarkModel is the declaration half of the property (types.md,
// Borrows 7; q137, EX3): marking a generic function of the prelude lent is
// refused at its T exactly when TestBorrowConvModel's model says its body
// keeps a value of T, and is otherwise silent. TestBorrowConvModel covers
// the call, which reads only the mark.
//
// Mutants it kills that no other test does (ledger: borrowtargs.go): the
// lent declaration not checked; each keep rule of keepWalk (a capture by
// an escaping frame, a pass to an unmarked type parameter, a function
// value's parameter, a function value captured) dropped or widened.
func TestLentMarkModel(t *testing.T) {
	sites := map[string]bcSite{}
	for _, s := range bcGenericSites {
		sites[s.name] = s
	}
	lines := strings.Split(borrowConvPrelude, "\n")
	for name, site := range bcLentSites {
		keeps := !sites[site].ok
		src := borrowConvPrelude
		at := 0
		for i, l := range lines {
			if strings.HasPrefix(l, "func "+name+"[T") {
				at = i + 1
			}
		}
		if at == 0 {
			t.Fatalf("%s: no declaration in the prelude", name)
		}
		src = strings.Replace(src, "func "+name+"[T](", "func "+name+"[T lent](", 1)
		src = strings.Replace(src, "func "+name+"[T Counter](", "func "+name+"[T lent Counter](", 1)
		src += "\nfunc main() {}\n"
		_, diags := CheckSource(src)
		var got []string
		for _, d := range diags {
			got = append(got, d.String())
		}
		ok := len(got) == 0
		if keeps {
			ok = len(diags) == 1 && diags[0].Code == "S108" && diags[0].Line == at &&
				strings.Contains(diags[0].Message, "T is lent, so the body keeps no value of it, but ")
		}
		if !ok {
			t.Errorf("%s marked lent (keeps %v by %s): got\n\t%s", name, keeps, site, strings.Join(got, "\n\t"))
		}
	}
}

// TestLentDeclarations pins the text and position of the declaration
// check of the lent mark (types.md, Borrows 7; q137, EX3): a lent type
// parameter whose body keeps a value of it, whose value goes to a type
// parameter not marked lent, of an extern, and of a type declaration;
// and the call's refusal names the mark to write, the constraint kept
// ([T lent Counter]).
// TestBorrowConvModel covers the call (a borrow binds only a lent T); it
// writes no body that breaks its mark.
//
// Mutants it kills that no other test does (ledger: borrowtargs.go): the
// declaration not checked; a lent T passed to an unmarked U admitted; a
// lent mark on an extern or a type admitted.
func TestLentDeclarations(t *testing.T) {
	src := `func keepIt[T lent](x T) T { x }

func pass[T lent](x T) T { let y = again(x)
	y }

func again[U](x U) U { x }

func plain[T lent](x T) int { 1 }

extern go slices.Clone func cloned[T lent](xs []T) []T

type Box[T lent] { V T }

// law-exempt: the test's operation type.
type Counter { Bump() int }

type Tally { n int }

func Bump(t *Tally) int { t.n }

func opPlain[T Counter](x T) int { x.Bump() }

func useOp(t *Tally) int { opPlain(t) }
`
	_, diags := CheckSource(src)
	var got []string
	for _, d := range diags {
		got = append(got, d.String())
	}
	want := []string{
		"1:13: S108 T is lent, so the body keeps no value of it, but it returns a value of it; drop lent, or keep no value of T (types.md, Borrows 7)",
		"3:11: S108 T is lent, so the body keeps no value of it, but it passes a value of it to again, and its signature does not write [U lent]; drop lent, or keep no value of T (types.md, Borrows 7)",
		"10:1: S108 the extern cloned has the type parameter T; a user extern writes the types it calls its Go target at, and Go infers the type arguments",
		"10:36: S108 T cannot be lent: an extern's body is Go code, which Ergo does not read (types.md, Borrows 7)",
		"12:10: S108 T cannot be lent: a value of a generic type holds its type argument, and a record or a collection never holds a borrow (types.md, Borrows 7)",
		"23:28: S108 opPlain cannot take a borrow as its T (*Tally): its signature does not write [T lent Counter], so its body may keep a value of T; a borrow instantiates only a type parameter whose signature writes lent (types.md, Borrows 7)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
