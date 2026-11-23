package lower

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/types"
	"maps"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The seeded property of OPTION-SHAPE (ADT-NOPRIV, design A, A1: the
// prelude's Option is one instance of the rule any generic union takes).
// Each seed writes one program of Options taken apart and built in the
// places a program's own code spells them: a generic result, a constant
// pattern, an Option of an Option, a tuple match, a record field and a
// slice element, a generic record's field, Go's == on two Options, a
// failure out of a match, and a loop over a slice of Options. It is
// written twice: over the prelude's Option, and over a user's copy of its
// declaration, Choice = Just | Nada, renamed word for word (names of the
// lengths of Option, Some and None, so no column moves), its types
// declared after every function, so no line moves.
//
// TestOptionIsOneUnion lowers both in process. Their IR is the same,
// function for function, once Choice, Just and Nada are spelled Option,
// Some and None (Option is no longer laid out apart: under the reference
// optionLowering "own" its slot is the record Some[T], read as a field,
// where a user union's is the shared slot, read by its as method). Their
// Go type-checks; every Option instance has the Go size and alignment of
// its form under the reference (the same bytes); and Option is the
// shared form under the rule and a slot per variant under the reference.
// TestFlatUnionsAgree's "own" mode runs the property's broader programs
// under the reference and compares what they print with the rule's.

var optionShapeSeed = flag.Uint64("optionshape.seed", 0, "run TestOptionIsOneUnion on this seed alone")

// optionShapePaths are the generator's statements; the fixed seeds reach
// each.
var optionShapePaths = []string{"find", "or", "same", "depth", "pair", "holder", "cell", "must", "loop", "first"}

const optionShapeDecls = `type Missing { At int }

type Holder { Name string, O Option[int], Xs []Option[int] }

type Cell[T] { v Option[T] }

func optionShapeAtIndex[T](xs []T, i int) Option[T] {
	if i < 0 || i >= len(xs) {
		return None
	}
	Some{xs[i]}
}

func pick(b bool, x int) Option[int] {
	if b { Some{x} } else { None }
}

func must(o Option[int]) int fails Missing {
	match o {
	case Some[int] as s:
		s.Value
	case None:
		return Missing{At: -1}
	}
}

func same[T comparable](a Option[T], b Option[T]) bool { a == b }

func or[T](o Option[T], d T) T {
	match o {
	case Some[T] as s:
		s.Value
	case None:
		d
	}
}

func show(o Option[int]) string {
	match o {
	case Some{Value: 3}:
		"three"
	case Some[int] as s:
		"some \(s.Value)"
	case None:
		"none"
	}
}

func depth(o Option[Option[int]]) string {
	match o {
	case Some{Value: Some{Value: 3}}:
		"deep three"
	case Some[Option[int]] as s:
		"outer " + show(s.Value)
	case None:
		"empty"
	}
}

func pair(a Option[int], b Option[int]) string {
	match (a, b) {
	case (Some[int] as x, Some[int] as y):
		"both \(x.Value + y.Value)"
	case (Some[int], None):
		"left"
	case (None, Some[int]):
		"right"
	case _:
		"neither"
	}
}

func firstOver(xs []int, lim int) Option[int] {
	for x in xs {
		if x > lim {
			return Some{x}
		}
	}
	None
}

`

// optionShapeMaybe is the user's copy of the prelude's Option, declared
// after the program in both forms (the Option form as a comment of as
// many lines), so every function keeps its position.
const optionShapeMaybe = "\ntype Just[T] { Value T }\n\ntype Nada {}\n\ntype Choice[T] = Just[T] | Nada\n"

// optionShapeProgram is the seed's program over the prelude's Option, and
// the paths it reached.
func optionShapeProgram(seed uint64, reached map[string]bool) string {
	r := rand.New(rand.NewPCG(seed, 0x6f707473))
	g := &optionGen{r: r, reached: map[string]bool{}}
	opt := func() string {
		switch r.IntN(3) {
		case 0:
			return fmt.Sprintf("optionShapeAtIndex(xs, %d)", g.index())
		case 1:
			return fmt.Sprintf("pick(%s, %d)", g.boolean(), g.small())
		}
		return fmt.Sprintf("firstOver(xs, %d)", g.small())
	}
	var b strings.Builder
	b.WriteString(optionShapeDecls)
	b.WriteString("func main() needs Console {\n\tlet xs = []int{1, 3, 5, 2}\n")
	order := slices.Clone(optionShapePaths)
	r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	for k := range 2 * len(order) {
		path := order[k%len(order)]
		if k >= len(order) {
			path = optionShapePaths[r.IntN(len(optionShapePaths))]
		}
		reached[path] = true
		n := k + 1
		switch path {
		case "find":
			fmt.Fprintf(&b, "\tConsole.Println(show(optionShapeAtIndex(xs, %d)))\n", g.index())
		case "or":
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(or(%s, -1))\")\n", opt())
		case "same":
			fmt.Fprintf(&b, "\tConsole.Println(\"\\(same(%s, %s))\")\n", opt(), opt())
		case "depth":
			inner := []string{"None", "Some{" + opt() + "}"}[r.IntN(2)]
			fmt.Fprintf(&b, "\tlet d%d Option[Option[int]] = %s\n\tConsole.Println(depth(d%d))\n", n, inner, n)
		case "pair":
			fmt.Fprintf(&b, "\tConsole.Println(pair(%s, %s))\n", opt(), opt())
		case "holder":
			fmt.Fprintf(&b, "\tlet h%d = Holder{Name: \"h\", O: %s, Xs: []Option[int]{%s, None}}\n\tConsole.Println(h%d.Name + \" \" + show(h%d.O) + \" \" + show(h%d.Xs[0]) + \" \" + show(h%d.Xs[1]))\n", n, opt(), opt(), n, n, n, n)
		case "cell":
			fmt.Fprintf(&b, "\tlet c%d = Cell[int]{v: %s}\n\tConsole.Println(show(c%d.v))\n", n, opt(), n)
		case "must":
			fmt.Fprintf(&b, "\tlet v%d = must(%s) catch {\n\tcase Missing as e:\n\t\te.At\n\t}\n\tConsole.Println(\"\\(v%d)\")\n", n, opt(), n)
		case "loop":
			fmt.Fprintf(&b, "\tfor o in []Option[int]{%s, %s, None} {\n\t\tConsole.Println(show(o))\n\t}\n", opt(), opt())
		case "first":
			fmt.Fprintf(&b, "\tConsole.Println(show(firstOver(xs, %d)))\n", g.small())
		}
	}
	b.WriteString("}\n")
	// As many lines as optionShapeMaybe, so the two forms' files have one
	// line count and every position is shared.
	b.WriteString(strings.Repeat("\n", strings.Count(optionShapeMaybe, "\n")))
	return b.String()
}

var (
	optionWord  = regexp.MustCompile(`\bOption\b`)
	someWord    = regexp.MustCompile(`\bSome\b`)
	noneWord    = regexp.MustCompile(`\bNone\b`)
	maybeWord   = regexp.MustCompile(`\bChoice\b`)
	justWord    = regexp.MustCompile(`\bJust\b`)
	nothingWord = regexp.MustCompile(`\bNada\b`)
)

// maybeOf is src over the user's Choice: Option, Some and None renamed, and
// Choice's declaration in the blank lines at the end.
func maybeOf(src string) string {
	s := noneWord.ReplaceAllString(someWord.ReplaceAllString(optionWord.ReplaceAllString(src, "Choice"), "Just"), "Nada")
	blank := strings.Repeat("\n", strings.Count(optionShapeMaybe, "\n"))
	return strings.TrimSuffix(s, blank) + optionShapeMaybe
}

// asOption is the IR of the Choice form spelled as the Option form's.
func asOption(ir string) string {
	return nothingWord.ReplaceAllString(justWord.ReplaceAllString(maybeWord.ReplaceAllString(ir, "Option"), "Some"), "None")
}

// optionShapeIR lowers src on the IR and gives each IR function's JSON by
// name, the names the report gives an IR body, and the Go text lowered.
func optionShapeIR(t *testing.T, name, src string) (funcs map[string]string, bodies []string, prog *lowered) {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	pkg, diags := check.Check("main", []*syntax.File{f}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v\n%s", diags, src)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	files, rep, err := LowerIR(u)
	if err != nil {
		t.Fatalf("LowerIR: %v\n%s", err, src)
	}
	funcs = map[string]string{}
	for _, file := range files {
		if file.Path != IRFileName {
			continue
		}
		var doc struct {
			Funcs []json.RawMessage `json:"funcs"`
		}
		if err := json.Unmarshal(file.Data, &doc); err != nil {
			t.Fatal(err)
		}
		for _, raw := range doc.Funcs {
			var head struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &head); err != nil {
				t.Fatal(err)
			}
			funcs[head.Name] = string(raw)
		}
	}
	return funcs, rep.IR, &lowered{name: name, src: src, file: f, pkg: pkg, files: files}
}

// optionAtExtern is a program whose Go extern's signature names an
// Option; maybeAtExtern the same shape on a user's union of Option's
// shape, which an extern names by value (cmp.Or returns its first
// argument that is not Go's zero value).
const optionAtExtern = `extern go os.LookupEnv func lookupEnv(key string) Option[string] needs Ambient

func main() needs Console | Ambient {
	match lookupEnv("ERGO_OPTION_SHAPE_UNSET") {
	case Some[string] as s:
		Console.Println("set " + s.Value)
	case None:
		Console.Println("unset")
	}
}
`

const maybeAtExtern = `type Just[T] { Value T }

type Nothing {}

type Maybe[T] = Just[T] | Nothing

extern go cmp.Or func firstSet(vals []Maybe[int]) Maybe[int] needs go.Foreign

func main() needs Console | go.Foreign {
	match firstSet([]Maybe[int]{Nothing, Just{4}}) {
	case Just[int] as j:
		Console.Println("set \(j.Value)")
	case Nothing:
		Console.Println("unset")
	}
}
`

// TestExternUnionsKeepTheirForm pins the forms a Go extern's signature
// gives a union of Option's shape until SHAPE-BOUNDARY (ADT-NOPRIV unit
// 3) makes the boundary one of shape (owner q271: any union of the
// shape). The prelude's Option stays flat where an extern names it, by
// the one named site of the rule (genericUnions; prelude-unions.txt): an
// extern's Option is an adapter's, built from Go's (v, ok), a nil or an
// error. A user's union of the same shape is an interface today, as every
// union an extern names is (its Go view); SHAPE-BOUNDARY flips this row
// to flat, its red-first check. Mutant option-extern-boxed.
func TestExternUnionsKeepTheirForm(t *testing.T) {
	for _, c := range []struct{ name, src, head, want string }{
		{"optionextern", optionAtExtern, "type Option[T any] ", "flat"},
		{"maybeextern", maybeAtExtern, "type Maybe[T any] ", "interface"}, // flat after SHAPE-BOUNDARY
	} {
		main := mustLower(t, c.name, c.src).mainGo()
		got := genericForm(main, c.head)
		if got != "interface" && got != "missing" {
			got = "flat"
		}
		if got != c.want {
			t.Errorf("%s: the union is %s, want %s\n%s", c.name, got, c.want, c.src)
		}
	}
}

// genericErrorUnions is a program with two generic unions that hold error
// types: Step, of which one variant (Bad, a failure of check) is one, and
// Lapse, of which every variant (Bad and Late[T], a failure of wait) is.
const genericErrorUnions = `type Bad { code int }

type Late[T] { at T }

type Ahead[T] { v T }

type Step[T] = Ahead[T] | Bad

type Lapse[T] = Bad | Late[T]

func check(n int) int fails Bad {
	if n < 0 {
		return Bad{code: n}
	}
	n
}

func wait(n int) int fails Late[int] {
	if n > 9 {
		return Late{at: n}
	}
	n
}

func step(n int) Step[int] {
	if n < 0 { Bad{code: n} } else { Ahead{v: n} }
}

func lapse(n int) Lapse[int] {
	if n < 0 { Bad{code: n} } else { Late{at: n} }
}

func main() needs Console {
	match step(-2) {
	case Ahead[int] as a:
		Console.Println("ahead \(a.v)")
	case Bad as b:
		Console.Println("bad \(b.code)")
	}
	match lapse(3) {
	case Bad as b:
		Console.Println("bad \(b.code)")
	case Late[int] as l:
		Console.Println("late \(l.at)")
	}
	let c = check(-1) catch {
	case Bad as b:
		b.code
	}
	let w = wait(12) catch {
	case Late[int] as l:
		l.at
	}
	Console.Println("\(c) \(w)")
}
`

// TestGenericErrorVariantsFlat is G6 of ADT-NOPRIV for generic unions, as
// MSB3 made it for the others (flatShape): a generic union of which only
// some variants are error types names no row, so it is flat, its error
// variants records in its slots; one whose every variant is an error type
// may name a row, which catch and ConvRow test by Go type, so it stays an
// interface. The reference msb3Lowering "off" keeps both interfaces. Each
// lowering type-checks. Mutants: g6-any-error-variant,
// generic-error-all-flat.
func TestGenericErrorVariantsFlat(t *testing.T) {
	rule := mustLower(t, "generr", genericErrorUnions)
	saved := msb3Lowering
	msb3Lowering = "off"
	ref := mustLower(t, "generrref", genericErrorUnions)
	msb3Lowering = saved
	for _, c := range []struct {
		prog       *lowered
		head, want string
	}{
		{rule, "type Step[T any] ", "flat"},
		{rule, "type Lapse[T any] ", "interface"},
		{ref, "type Step[T any] ", "interface"},
		{ref, "type Lapse[T any] ", "interface"},
	} {
		got := genericForm(c.prog.mainGo(), c.head)
		if got != "interface" && got != "missing" {
			got = "flat"
		}
		if got != c.want {
			t.Errorf("%s: %sis %s, want %s", c.prog.name, c.head, got, c.want)
		}
	}
	typecheckGo(t, []*lowered{rule, ref})
}

// concrete reports whether t names no type parameter (a generic body's
// Option[T] has no size).
func concrete(t types.Type) bool {
	switch t := t.(type) {
	case *types.TypeParam:
		return false
	case *types.Named:
		for i := range t.TypeArgs().Len() {
			if !concrete(t.TypeArgs().At(i)) {
				return false
			}
		}
	case *types.Slice:
		return concrete(t.Elem())
	case *types.Array:
		return concrete(t.Elem())
	case *types.Pointer:
		return concrete(t.Elem())
	case *types.Map:
		return concrete(t.Key()) && concrete(t.Elem())
	case *types.Struct:
		for i := range t.NumFields() {
			if !concrete(t.Field(i).Type()) {
				return false
			}
		}
	}
	return true
}

// windowDiff is the text around the first byte where a and b differ.
func windowDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-60)
	return fmt.Sprintf("at byte %d: want …%s…, got …%s…", i, a[from:min(len(a), i+60)], b[from:min(len(b), i+60)])
}

// optionSizes are the Go size and alignment of each instance of the Go
// generic type named head ("Option", "Maybe") the program spells, by its
// type arguments, from go/types on the lowered Go.
func optionSizes(t *testing.T, prog *lowered, head string) map[string][2]int64 {
	t.Helper()
	infos := map[string]*types.Info{}
	failed, err := typecheckArch(t, []*lowered{prog}, "amd64", infos)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed[prog.name]) > 0 {
		t.Fatalf("%s does not type-check: %v", prog.name, failed[prog.name])
	}
	sizes := types.SizesFor("gc", "amd64")
	out := map[string][2]int64{}
	for _, tv := range infos[prog.name].Types {
		n, ok := tv.Type.(*types.Named)
		if !ok || n.Obj().Name() != head || n.TypeArgs().Len() == 0 || !concrete(n) {
			continue
		}
		if _, ok := n.Underlying().(*types.Struct); !ok {
			continue
		}
		var args []string
		for i := range n.TypeArgs().Len() {
			// Unqualified, and in the Option form's names, so the two
			// forms' instances are keyed alike.
			args = append(args, asOption(types.TypeString(n.TypeArgs().At(i), func(*types.Package) string { return "" })))
		}
		out[strings.Join(args, ",")] = [2]int64{sizes.Sizeof(n), sizes.Alignof(n)}
	}
	return out
}

// TestOptionIsOneUnion is the in-process property (see above), AC5 of
// ADT-NOPRIV. Mutant (testdata/mutants.json): option-own-shape.
func TestOptionIsOneUnion(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *optionShapeSeed != 0 {
		seeds = []uint64{*optionShapeSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		src := optionShapeProgram(seed, reached)
		replay := fmt.Sprintf("Replay: go test -run '^TestOptionIsOneUnion$' ./internal/sketch/lower -args -optionshape.seed=%d", seed)
		optFuncs, optBodies, optProg := optionShapeIR(t, fmt.Sprintf("optshape%d", seed), src)
		mayFuncs, _, mayProg := optionShapeIR(t, fmt.Sprintf("mayshape%d", seed), maybeOf(src))
		for _, fn := range []string{"optionShapeAtIndex", "pick", "must", "or", "show", "depth", "pair", "firstOver"} {
			if !slices.Contains(optBodies, fn) {
				t.Fatalf("seed %d: %s has no IR body, so its IR is not compared (IR bodies %v)\n%s", seed, fn, optBodies, replay)
			}
		}
		for _, fn := range slices.Sorted(func(yield func(string) bool) {
			for k := range optFuncs {
				if !yield(k) {
					return
				}
			}
		}) {
			may, ok := mayFuncs[fn]
			if !ok {
				t.Errorf("seed %d: %s is an IR body over Option and not over Choice\n%s", seed, fn, replay)
				continue
			}
			if got, want := asOption(may), optFuncs[fn]; got != want {
				t.Errorf("seed %d: the IR of %s over Choice differs from its IR over Option: %s\n%s\n%s", seed, fn, windowDiff(want, got), replay, src)
			}
		}
		// One form: Option's is the general rule's, the shared slot, as
		// Choice's is, with the same Go size and alignment per instance.
		for _, h := range []struct {
			prog *lowered
			head string
		}{{optProg, "Option"}, {mayProg, "Choice"}} {
			if got, want := genericForm(h.prog.mainGo(), "type "+h.head+"[T any] "), "shared 1"; got != want {
				t.Errorf("seed %d: %s is %s, want %s (the general rule's form)\n%s", seed, h.head, got, want, replay)
			}
		}
		rule, maybe := optionSizes(t, optProg, "Option"), optionSizes(t, mayProg, "Choice")
		if len(rule) == 0 || !maps.Equal(rule, maybe) {
			t.Errorf("seed %d: the Go sizes of Option's instances are %v and of Choice's %v, want one, not empty\n%s", seed, rule, maybe, replay)
		}
	}
	if *optionShapeSeed != 0 {
		return
	}
	for _, path := range optionShapePaths {
		if !reached[path] {
			t.Errorf("the fixed seeds never reach %q", path)
		}
	}
}
