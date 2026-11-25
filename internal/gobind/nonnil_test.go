package gobind_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// nnForm is a part of a generated (*S, error) body, and the model's
// verdict: whether the binder's rule (nonnil.go) proves it never returns
// a nil pointer with a nil error. Every form the model proves is safe by
// Go's semantics; a form it does not prove may be safe too (the rule is
// conservative), which the comment names.
type nnForm struct {
	name   string
	text   string // A for the int parameter
	proven bool
}

// nnEarly are the guarded early returns, `if A > k { <text> }`.
var nnEarly = []nnForm{
	{"errors-new", "return nil, errors.New(\"e\")", true},
	{"fmt-errorf", "return nil, fmt.Errorf(\"e %d\", A)", true},
	{"sentinel", "return nil, errSentinel", true},
	{"moved-sentinel", "return nil, errMoved", false}, // reset() assigns it nil
	{"composite-error", "return nil, &E{}", true},
	{"value-error", "return nil, V{}", true},
	{"nil-nil", "return nil, nil", false}, // ProxyFromEnvironment's shape
	{"error-call", "return nil, errOf(A)", false},
	{"lit", "return &S{X: A}, nil", true},
	{"new", "return new(S), nil", true},
	{"fresh", "return fresh(A), nil", true},
	{"maybe", "return maybe(A), nil", false},
	{"guarded", "if err := errOf(A); err != nil { return nil, err }", true},
	{"guard-reset", "if err := errOf(A); err != nil { err = nil; return nil, err }", false},
	{"guard-and", "if err := errOf(A); err != nil && A > 3 { return nil, err }", true},
	{"guard-or", "if err := errOf(A); err != nil || A > 3 { return nil, err }", false},
	{"closure", "_ = func() (*S, error) { return nil, nil }", true},
	// GOBIND-PROVE.
	{"conversion", "return nil, Code(A)", true},
	{"interface-conversion", "return nil, EI(errOf(A))", false}, // type EI error: an interface
	{"const-error", "return nil, ErrConst", true},
	{"error-func", "return nil, wrapped(A)", true},
	{"typed-nil", "if A > 1 { var pe *E; return nil, pe }", true}, // a nil *E in an error is no nil error
	{"interface-var", "if A > 1 { var ie EI = errOf(A); return nil, ie }", false},
	{"path-guard", "if t0.err != nil { return nil, t0.err }", true},
	{"path-guard-late", "if t0.err != nil { t0.reset(); return nil, t0.err }", false},
	// The variable rule reads a name across the whole body, so a guard's
	// init variables take names no last statement uses.
	{"init-guard", "if q, err := mk(A); err == nil { return q, nil }", true},
	{"init-guard-maybe", "if q, err := mkMaybe(A); err == nil { return q, nil }", false},
	{"init-guard-neq", "if q, err := mk(A); err != nil { return q, nil }", false},
	{"nonnil-guard", "if p := maybe(A); p != nil { return p, nil }", true},
	{"nil-guard", "if p := maybe(A); p == nil { return p, nil }", false},
}

// nnFinal are the last statements.
var nnFinal = []nnForm{
	{"lit", "return &S{X: A}, nil", true},
	{"delegate", "return mk(A)", true},
	{"delegate-maybe", "return mkMaybe(A)", false},
	{"func-value", "return hook(A)", false}, // a function value: ProxyFromEnvironment
	{"checked", "s, err := mk(A)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn s, nil", true},
	{"checked-maybe", "s, err := mkMaybe(A)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn s, nil", false},
	{"pair", "s, err := mk(A)\n\treturn s, err", true}, // one proven call's two results
	{"pair-maybe", "s, err := mkMaybe(A)\n\treturn s, err", false},
	{"pair-broken", "s, err := mk(A)\n\terr = errOf(A)\n\treturn s, err", false},
	{"pkg-pointer", "return defaultS, nil", true},
	{"pkg-pointer-set", "return movedS, nil", false}, // resetS() assigns it nil
	{"method-local", "b := &builder{}\n\treturn b.build(A)", true},
	{"method-local-maybe", "b := &builder{}\n\treturn b.buildMaybe(A)", false},
	{"method-pkgvar", "return defaultB.build(A)", true},
	{"var-maybe", "var s *S\n\tif A > 1 {\n\t\ts = &S{}\n\t}\n\treturn s, nil", false},
	{"var-lit", "s := &S{}\n\ts.X = A\n\treturn s, nil", true},
	{"var-rebound", "s := &S{}\n\tif A > 2 {\n\t\ts = maybe(A)\n\t}\n\treturn s, nil", false},
	{"var-address", "s := &S{}\n\tkeep(&s)\n\treturn s, nil", false},
	{"panic", "panic(\"no\")", true},
	{"recursion", "if A > 0 {\n\t\treturn SELF(A - 1)\n\t}\n\treturn &S{}, nil", false}, // safe, but its verdict reads itself: the least fixed point
	// GOBIND-FLOW.
	{"nil-check", "s := maybe(A)\n\tif s == nil {\n\t\treturn nil, errors.New(\"nil\")\n\t}\n\treturn s, nil", true},
	{"nil-check-refix", "s := maybe(A)\n\tif s == nil {\n\t\ts = maybe(A + 1)\n\t}\n\treturn s, nil", false},
	{"deref", "s := maybe(A)\n\ts.X = A\n\treturn s, nil", true},                              // a nil s panics at s.X
	{"deref-or", "s := maybe(A)\n\tok := A > 1 || s.X > 0\n\t_ = ok\n\treturn s, nil", false}, // s.X may not run
	{"panic-branch", "var s *S\n\tif A > 1 {\n\t\ts = &S{}\n\t} else {\n\t\tpanic(\"no\")\n\t}\n\treturn s, nil", true},
	{"deref-method", "s := maybe(A)\n\t_ = s.Self\n\treturn s, nil", false}, // a pointer method's value takes a nil s
	{"join", "var s *S\n\tvar err error\n\tif A > 1 {\n\t\ts, err = mk(A)\n\t} else {\n\t\ts, err = mk(A + 1)\n\t}\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn s, nil", true},
	{"join-maybe", "var s *S\n\tvar err error\n\tif A > 1 {\n\t\ts, err = mk(A)\n\t} else {\n\t\ts, err = mkMaybe(A)\n\t}\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn s, nil", false},
	{"first-error", "var first error\n\tif q, err := mk(A); err == nil {\n\t\treturn q, nil\n\t} else if first == nil {\n\t\tfirst = err\n\t}\n\treturn nil, first", true}, // time.LoadLocation's shape
	{"first-error-unset", "var first error\n\tif q, err := mk(A); err == nil {\n\t\treturn q, nil\n\t}\n\treturn nil, first", false},
	{"error-wrapped", "s, err := mk(A)\n\tif err != nil {\n\t\terr = fmt.Errorf(\"w %v\", err)\n\t}\n\treturn s, err", true},
	{"error-rewrapped", "s, err := mk(A)\n\tif err != nil {\n\t\terr = errOf(A)\n\t}\n\treturn s, err", false},
	{"loop", "s := &S{}\n\tfor i := 0; i < A; i++ {\n\t\tif i > 2 {\n\t\t\tbreak\n\t\t}\n\t\ts.X++\n\t}\n\treturn s, nil", true},
	{"loop-rebound", "s := &S{}\n\tfor i := 0; i < A; i++ {\n\t\ts = maybe(i)\n\t}\n\treturn s, nil", false},
	{"shadow", "s := &S{}\n\tif A > 1 {\n\t\ts := maybe(A)\n\t\t_ = s\n\t}\n\treturn s, nil", true},
	{"captured", "s := &S{}\n\tfunc() { s = maybe(A) }()\n\treturn s, nil", false},
	{"captured-checked", "s := maybe(A)\n\tfunc() { s = &S{} }()\n\tif s == nil {\n\t\treturn nil, errors.New(\"nil\")\n\t}\n\treturn s, nil", true},
	{"switch", "var s *S\n\tswitch {\n\tcase A > 1:\n\t\ts = &S{}\n\tdefault:\n\t\ts = new(S)\n\t}\n\treturn s, nil", true},
	{"switch-open", "var s *S\n\tswitch {\n\tcase A > 1:\n\t\ts = &S{}\n\t}\n\treturn s, nil", false},
	{"goto-skip", "s := maybe(A)\n\tif s == nil {\n\t\tgoto done\n\t}\n\ts.X = A\ndone:\n\treturn s, nil", false},
	{"loop-break", "s := &S{}\n\tfor i := 0; i < A; i++ {\n\t\tif i > 2 {\n\t\t\ts = maybe(i)\n\t\t\tbreak\n\t\t}\n\t}\n\treturn s, nil", false},
	{"pair-nil-err", "s, err := mk(A)\n\tif s == nil {\n\t\treturn nil, err\n\t}\n\treturn s, nil", true},
	{"pair-nil-check", "s, err := mk(A)\n\tif s == nil {\n\t\treturn s, nil\n\t}\n\treturn s, err", false},
	{"goto-retry", "s := maybe(A)\nagain:\n\tif s == nil {\n\t\ts = &S{}\n\t\tgoto again\n\t}\n\treturn s, nil", true},
	{"field-set", "h := &holder{}\n\th.err = wrapped(A)\n\treturn nil, h.err", true},
	{"field-set-call", "h := &holder{}\n\th.err = wrapped(A)\n\th.reset()\n\treturn nil, h.err", false},
	{"recover", "defer func() { recover() }()\n\treturn fresh(A), nil", false}, // safe here, but a recovered panic returns nil, nil
	// GOBIND-CLOSURE-SOUND: a literal the flow cannot see run makes what
	// it assigns unknown everywhere.
	{"closure-held", "s := maybe(A)\n\tg := func() { s = &S{} }\n\tg()\n\tif s == nil {\n\t\treturn nil, errors.New(\"nil\")\n\t}\n\treturn s, nil", true},
	{"closure-stored", "s := &S{}\n\tb := &box{}\n\tb.f = func() { s = maybe(A) }\n\ts = &S{}\n\tb.f()\n\treturn s, nil", false},
	{"closure-aliased", "s := &S{}\n\tg := func() { s = maybe(A) }\n\tk := g\n\ts = &S{}\n\tk()\n\treturn s, nil", false},
	{"closure-passed", "s := &S{}\n\tg := func() { s = maybe(A) }\n\ts = &S{}\n\trun(g)\n\treturn s, nil", false},
	{"closure-nested", "s := &S{}\n\tg := func() { s = maybe(A) }\n\tb := &box{f: func() { g() }}\n\ts = &S{}\n\tb.f()\n\treturn s, nil", false},
	{"closure-inner", "s := &S{}\n\tb := &box{}\n\tb.f = func() { func() { s = maybe(A) }() }\n\ts = &S{}\n\tb.f()\n\treturn s, nil", false},
	// GOBIND-LOOP: a range over a collection known non-empty runs its
	// body at least once (template.parseFiles).
	{"range-nonempty", "var xs []int\n\tif A > 1 {\n\t\txs = []int{A}\n\t}\n\tvar s *S\n\tif len(xs) == 0 {\n\t\treturn nil, errors.New(\"empty\")\n\t}\n\tfor _, x := range xs {\n\t\ts = fresh(x)\n\t}\n\treturn s, nil", true},
	{"range-maybe-empty", "var xs []int\n\tif A > 1 {\n\t\txs = []int{A}\n\t}\n\tvar s *S\n\tfor _, x := range xs {\n\t\ts = fresh(x)\n\t}\n\treturn s, nil", false},
	{"range-len-stale", "var xs []int\n\tif A > 1 {\n\t\txs = []int{A}\n\t}\n\tvar s *S\n\tif len(xs) == 0 {\n\t\treturn nil, errors.New(\"empty\")\n\t}\n\txs = nil\n\tfor _, x := range xs {\n\t\ts = fresh(x)\n\t}\n\treturn s, nil", false},
	{"range-len-gt", "var xs []int\n\tif A > 1 {\n\t\txs = []int{A}\n\t}\n\tvar s *S\n\tif len(xs) > 0 {\n\t\tfor _, x := range xs {\n\t\t\ts = fresh(x)\n\t\t}\n\t\treturn s, nil\n\t}\n\treturn nil, errors.New(\"empty\")", true},
	{"range-len-lt", "var xs []int\n\tif A > 1 {\n\t\txs = []int{A}\n\t}\n\tvar s *S\n\tif 0 < len(xs) {\n\t\tfor _, x := range xs {\n\t\t\ts = fresh(x)\n\t\t}\n\t\treturn s, nil\n\t}\n\treturn nil, errors.New(\"empty\")", true},
	{"range-len-under", "var xs []int\n\tif A > 1 {\n\t\txs = []int{A}\n\t}\n\tvar s *S\n\tif len(xs) < 0 {\n\t\tfor _, x := range xs {\n\t\t\ts = fresh(x)\n\t\t}\n\t\treturn s, nil\n\t}\n\treturn nil, errors.New(\"empty\")", false},
	{"range-chan", "var ch chan int = make(chan int, A)\n\tvar s *S\n\tif len(ch) == 0 {\n\t\treturn nil, errors.New(\"empty\")\n\t}\n\tfor x := range ch {\n\t\ts = fresh(x)\n\t\tbreak\n\t}\n\treturn s, nil", false}, // another receiver may drain it
	{"range-len-local", "var xs []int\n\tlen := func([]int) int { return 1 }\n\tvar s *S\n\tif len(xs) == 0 {\n\t\treturn nil, errors.New(\"empty\")\n\t}\n\tfor _, x := range xs {\n\t\ts = fresh(x)\n\t}\n\treturn s, nil", false},
	{"closure-go", "s := &S{}\n\tgo func() { s = maybe(A) }()\n\ts = &S{}\n\treturn s, nil", false},
}

// TestNonNilResultsProperty generates Go packages of (*S, error)
// functions and methods whose bodies join guarded early returns and a last
// statement of known verdicts, binds them with ergo bind's own path on
// every platform, and holds each binding to the model: go.Ref[S] exactly
// where the model proves the pointer non-nil when the error is nil, and
// Option[go.Ref[S]] elsewhere (GOBIND-NIL: http.ProxyFromEnvironment, a
// function value's call, returns nil, nil). A failing seed prints the Go
// source and the binding; ERGO_NONNIL_SEED replays one seed. No binary is
// built: go build -gcflags compiles the small packages, as Derive does.
//
// Mutants it alone catches: gobind-nonnil-any-return, gobind-nonnil-nil-error,
// gobind-nonnil-any-call, gobind-nonnil-any-error-call, gobind-nonnil-or-guard,
// gobind-nonnil-guard-reassigned, gobind-nonnil-var-any-assign,
// gobind-nonnil-moved-sentinel, gobind-nonnil-unchecked-call,
// gobind-nonnil-greatest-fixpoint, gobind-nonnil-type-promises, the
// gobind-prove-* rows of GOBIND-PROVE (interface conversions and values,
// constants, error functions, typed nils, field paths, pairs, package
// pointers), and the gobind-flow-* rows of GOBIND-FLOW (joins, loops,
// breaks, dead nil checks, pair refinement, dereferences, closures,
// gotos, defers, field stores, open switches, scopes, panics), and the
// gobind-closure-* rows of GOBIND-CLOSURE-SOUND (escaping literals).
func TestNonNilResultsProperty(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv("ERGO_NONNIL_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("ERGO_NONNIL_SEED: %v", err)
		}
		seeds = []uint64{n}
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.test/nonnil\n\ngo 1.27\n")
	type pkg struct {
		path, src string
		model     map[string]bool
	}
	var pkgs []pkg
	var patterns []string
	reached := map[string]bool{}
	for _, seed := range seeds {
		name := fmt.Sprintf("n%d", seed)
		src, model := genNonNil(name, rand.New(rand.NewPCG(seed, 0x4e11)), reached)
		write(t, filepath.Join(dir, name, name+".go"), src)
		pkgs = append(pkgs, pkg{"example.test/nonnil/" + name, src, model})
		patterns = append(patterns, "./"+name)
	}
	if len(seeds) > 1 {
		var missed []string
		for _, f := range slices.Concat(nnEarly, nnFinal) {
			if !reached[f.name] {
				missed = append(missed, f.name)
			}
		}
		if len(missed) > 0 {
			t.Fatalf("the seeds reach no function with the forms %v", missed)
		}
	}
	files, err := gobind.Bind(dir, os.Environ(), "go1.27", patterns...)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		got := pointerResults(p.path, files[p.path])
		var bad []string
		if !strings.Contains(files[p.path], "Make() option.Option[go.Ref[S]] fails go.Error") {
			bad = append(bad, "Maker.Make: an interface method proves nothing, want option.Option[go.Ref[S]]")
		}
		for fn, proven := range p.model {
			ref, ok := got[fn]
			switch {
			case !ok:
				bad = append(bad, fn+": not bound with a (*S, error) result")
			case ref != proven:
				bad = append(bad, fmt.Sprintf("%s: go.Ref %v, want %v", fn, ref, proven))
			}
		}
		if len(bad) > 0 {
			slices.Sort(bad)
			t.Errorf("seed package %s (replay: ERGO_NONNIL_SEED=%s):\n%s\n--- Go source:\n%s\n--- binding:\n%s", p.path, strings.TrimPrefix(filepath.Base(p.path), "n"), strings.Join(bad, "\n"), p.src, files[p.path])
		}
	}
}

var pointerResultRe = regexp.MustCompile(`^extern go (\S+) .*\) (go\.Ref\[S\]|Option\[go\.Ref\[S\]\]) fails go\.Error`)

// pointerResults reads, by binding name (F, or S.M), whether each bound
// function's (*S, error) result is go.Ref[S] (true) or Option (false).
func pointerResults(path, text string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if m := pointerResultRe.FindStringSubmatch(line); m != nil {
			out[strings.TrimPrefix(m[1], path+".")] = m[2] == "go.Ref[S]"
		}
	}
	return out
}

// genNonNil writes a Go package of exported (*S, error) functions and
// methods, and the model's verdict for each by binding name. reached
// collects the forms the package uses.
func genNonNil(name string, r *rand.Rand, reached map[string]bool) (string, map[string]bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\n", name)
	b.WriteString(`type S struct{ X int }

type E struct{}

func (*E) Error() string { return "E" }

type V struct{}

func (V) Error() string { return "V" }

var errSentinel = errors.New("sentinel")

var errMoved = errors.New("moved")

func reset() { errMoved = nil }

var _ = reset

func errOf(a int) error {
	if a > 2 {
		return errors.New("odd")
	}
	return nil
}

func fresh(a int) *S { return &S{X: a} }

func maybe(a int) *S {
	if a > 0 {
		return nil
	}
	return &S{}
}

func mk(a int) (*S, error) {
	if a < 0 {
		return nil, errors.New("negative")
	}
	return &S{X: a}, nil
}

func mkMaybe(a int) (*S, error) {
	if a < 0 {
		return nil, nil
	}
	return &S{X: a}, nil
}

var hook = mk

func keep(p **S) { *p = nil }

type Code int

func (c Code) Error() string { return fmt.Sprint(int(c)) }

type EI error

const ErrConst = Code(7)

func wrapped(a int) error { return fmt.Errorf("w%d", a) }

type holder struct{ err error }

func (h *holder) reset() { h.err = nil }

var t0 = &holder{}

var defaultS = &S{}

var movedS = &S{}

func resetS() { movedS = nil }

var _ = resetS

type builder struct{}

func (b *builder) build(a int) (*S, error) { return &S{X: a}, nil }

func (b *builder) buildMaybe(a int) (*S, error) {
	if a > 0 {
		return nil, nil
	}
	return &S{}, nil
}

var defaultB = &builder{}

type box struct{ f func() }

func run(f func()) { f() }

`)
	model := map[string]bool{}
	// Each package reaches every form as the subject of a function whose
	// other parts the model mostly proves, so the subject decides its
	// verdict; one function in three draws its other parts from every form.
	pick := func(forms []nnForm, any bool) nnForm {
		for {
			f := forms[r.IntN(len(forms))]
			if any || f.proven {
				return f
			}
		}
	}
	n := len(nnEarly) + len(nnFinal)
	offset := r.IntN(n)
	for i := 0; i < n; i++ {
		fn := fmt.Sprintf("F%d", i)
		key, recv, self := fn, "", fn
		if r.IntN(4) == 0 {
			key, recv, self = "S."+fn, "(t *S) ", "t."+fn
		}
		subject := (i + offset) % n
		any := r.IntN(3) == 0
		var early []nnForm
		if subject < len(nnEarly) {
			early = append(early, nnEarly[subject])
		}
		for j := r.IntN(2); j > 0; j-- {
			early = append(early, pick(nnEarly, any))
		}
		r.Shuffle(len(early), func(a, b int) { early[a], early[b] = early[b], early[a] })
		f := pick(nnFinal, any)
		if subject >= len(nnEarly) {
			f = nnFinal[subject-len(nnEarly)]
		}
		proven := true
		var body []string
		for _, e := range early {
			reached[e.name] = true
			proven = proven && e.proven
			stmt := strings.ReplaceAll(e.text, "A", "a")
			if !strings.HasPrefix(stmt, "if ") && !strings.HasPrefix(stmt, "_ =") {
				stmt = fmt.Sprintf("if a > %d { %s }", r.IntN(5), stmt)
			}
			body = append(body, stmt)
		}
		reached[f.name] = true
		proven = proven && f.proven
		body = append(body, strings.ReplaceAll(strings.ReplaceAll(f.text, "A", "a"), "SELF", self))
		fmt.Fprintf(&b, "func %s%s(a int) (*S, error) {\n\t%s\n}\n\n", recv, fn, strings.Join(body, "\n\t"))
		model[key] = proven
	}
	// A receiver may be nil. A named result holds what the flow last
	// gave it (GOBIND-FLOW), and a deferred literal may only set it
	// non-nil.
	b.WriteString("func (s *S) Self() (*S, error) { return s, nil }\n\n")
	b.WriteString("func Named(a int) (r *S, err error) {\n\tr = &S{X: a}\n\treturn\n}\n\n")
	b.WriteString("func NamedEarly(a int) (r *S, err error) {\n\tif a > 0 {\n\t\treturn\n\t}\n\tr = &S{X: a}\n\treturn\n}\n\n")
	b.WriteString("func NamedDefer(a int) (r *S, err error) {\n\tdefer func() {\n\t\tif err != nil {\n\t\t\terr = fmt.Errorf(\"w: %w\", err)\n\t\t}\n\t}()\n\tr = &S{X: a}\n\treturn r, nil\n}\n\n")
	b.WriteString("func NamedDeferNil(a int) (r *S, err error) {\n\tdefer func() {\n\t\tif a > 1 {\n\t\t\tr = nil\n\t\t}\n\t}()\n\treturn &S{X: a}, nil\n}\n\n")
	// Where err is nil the return gave a pointer, so a deferred literal
	// may set err to anything there (multipart.Reader.ReadForm).
	b.WriteString("func NamedDeferClose(a int) (r *S, err error) {\n\tdefer func() {\n\t\tif cerr := errOf(a); err == nil {\n\t\t\terr = cerr\n\t\t}\n\t}()\n\treturn &S{X: a}, nil\n}\n\n")
	b.WriteString("func NamedDeferAny(a int) (r *S, err error) {\n\tdefer func() {\n\t\tif err != nil {\n\t\t\terr = errOf(a)\n\t\t}\n\t}()\n\tif a > 0 {\n\t\treturn nil, errors.New(\"x\")\n\t}\n\treturn &S{X: a}, nil\n}\n\n")
	b.WriteString("type Maker interface{ Make() (*S, error) }\n\n")
	model["S.Self"], model["Named"], model["NamedEarly"], model["NamedDefer"], model["NamedDeferNil"] = false, true, false, true, false
	b.WriteString("func NamedBlank(a int) (_ *S, err error) {\n\th := &holder{}\n\tdefer func() {\n\t\th.err = nil\n\t}()\n\treturn &S{X: a}, nil\n}\n\n")
	model["NamedDeferClose"], model["NamedDeferAny"], model["NamedBlank"] = true, false, true
	// A literal deferred through a variable may set a named result after
	// the return (GOBIND-CLOSURE-SOUND).
	b.WriteString("func NamedDeferHeld(a int) (r *S, err error) {\n\tg := func() { r = nil }\n\tdefer g()\n\treturn &S{X: a}, nil\n}\n\n")
	model["NamedDeferHeld"] = false
	// A variadic parameter is a collection (template.parseFiles).
	b.WriteString("func Files(t *S, names ...string) (*S, error) {\n\tif len(names) == 0 {\n\t\treturn nil, errors.New(\"none\")\n\t}\n\tfor range names {\n\t\tif t == nil {\n\t\t\tt = &S{}\n\t\t}\n\t}\n\treturn t, nil\n}\n\n")
	model["Files"] = true
	// A parameter's nil default fixed at the top level (GOBIND-PROVE):
	// after `if s == nil { s = &S{} }` s is non-nil, unless the fix may
	// set nil or a return comes before it.
	b.WriteString("func Fix(s *S) (*S, error) {\n\tif s == nil {\n\t\ts = &S{}\n\t}\n\treturn s, nil\n}\n\n")
	b.WriteString("func FixMaybe(s *S, a int) (*S, error) {\n\tif s == nil {\n\t\ts = maybe(a)\n\t}\n\treturn s, nil\n}\n\n")
	b.WriteString("func FixAfter(s *S, a int) (*S, error) {\n\tif a > 0 {\n\t\treturn s, nil\n\t}\n\tif s == nil {\n\t\treturn nil, errors.New(\"nil\")\n\t}\n\treturn s, nil\n}\n\n")
	b.WriteString("func FixEnds(s *S) (*S, error) {\n\tif s == nil {\n\t\treturn nil, errors.New(\"nil\")\n\t}\n\treturn s, nil\n}\n\n")
	model["Fix"], model["FixMaybe"], model["FixAfter"], model["FixEnds"] = true, false, false, true
	return b.String(), model
}
