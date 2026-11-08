package std_test

import (
	"go/token"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The Seq stop rule (docs/spec/stdlib.md, Std laws 5; std-strengths SS2):
// each exported function and operation of std whose result is the
// prelude's Seq states that it stops, as a law named <Producer>Stops, or
// carries one doc-comment line `// seq-exempt: <reason>`.

// seqExemptPrefix starts the one doc-comment line that exempts a Seq
// producer from the rule. The text after it is the reason, never empty.
const seqExemptPrefix = "// seq-exempt:"

// seqProducer is one exported function or operation whose result is the
// prelude's Seq.
type seqProducer struct {
	name   string // the producer's name in its law: Values, InputLines, Values of Queue
	owner  *check.TypeName
	doc    *syntax.CommentGroup
	report string // the name in a violation: pkg.Func, pkg.Type.Op
}

// isSeq reports whether t is an instance of the prelude's Seq.
func isSeq(t check.Type, seq *check.TypeName) bool {
	n, ok := t.(*check.Named)
	if !ok {
		return false
	}
	if n.Orig != nil {
		n = n.Orig
	}
	return n.Obj == seq
}

// seqResult reports whether sig has one result, the prelude's Seq.
func seqResult(sig *check.Signature, seq *check.TypeName) bool {
	return sig != nil && len(sig.Results) == 1 && isSeq(sig.Results[0], seq)
}

// seqProducers are pkg's exported functions and operations whose result is
// the prelude's Seq, found by the checker's types, never from a list. A
// function whose first parameter is a type of pkg (a function x.f(…)
// reaches) is named by that type and itself: io's Lines(input Input) is
// InputLines.
func seqProducers(path string, pkg *check.Package, seq *check.TypeName) []seqProducer {
	var out []seqProducer
	for _, f := range pkg.Funcs {
		if !token.IsExported(f.Name) || f.Decl == nil || !seqResult(f.Sig, seq) {
			continue
		}
		name, report := f.Name, path+"."+f.Name
		if len(f.Sig.Params) > 0 {
			if n, ok := f.Sig.Params[0].Type.(*check.Named); ok && n.Obj != nil && n.Obj.Pkg == pkg {
				name = n.Obj.Name + f.Name
				report = path + "." + f.Name + "(" + n.Obj.Name + ")"
			}
		}
		out = append(out, seqProducer{name: name, doc: f.Decl.Doc, report: report})
	}
	for _, tn := range operationTypes(pkg) {
		it := tn.Type.(*check.Named).Underlying().(*check.Interface)
		spec, _ := tn.Decl.Spec.(*syntax.InterfaceType)
		for _, m := range it.Methods {
			if !token.IsExported(m.Name) || !seqResult(m.Sig, seq) {
				continue
			}
			var doc *syntax.CommentGroup
			if spec != nil {
				for _, field := range spec.Methods {
					if len(field.Names) > 0 && field.Names[0].Name == m.Name {
						doc = field.Doc
					}
				}
			}
			out = append(out, seqProducer{name: m.Name, owner: tn, doc: doc, report: path + "." + tn.Name + "." + m.Name})
		}
	}
	return out
}

// seqExemption is the reason of a seq-exempt line in doc and whether doc
// has one.
func seqExemption(doc *syntax.CommentGroup) (string, bool) {
	if doc == nil {
		return "", false
	}
	for _, c := range doc.List {
		if rest, ok := strings.CutPrefix(c.Text, seqExemptPrefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// hasStopLaw reports whether pkg states the stop law of p: a top-level law
// <name>Stops for a function, a member law <name>Stops of its type for an
// operation.
func hasStopLaw(pkg *check.Package, p seqProducer) bool {
	want := p.name + "Stops"
	if p.owner != nil {
		return slices.ContainsFunc(p.owner.Laws, func(l *check.Law) bool { return l.Name == want })
	}
	return slices.ContainsFunc(pkg.Laws, func(l *check.Law) bool { return l.Owner == nil && l.Name == want })
}

// seqRuleViolations are the Seq producers of pkg that break the stop rule,
// each with the way it breaks it.
func seqRuleViolations(path string, pkg *check.Package, seq *check.TypeName) []string {
	var out []string
	for _, p := range seqProducers(path, pkg, seq) {
		law := p.name + "Stops"
		if p.owner != nil {
			law = p.owner.Name + "." + law
		}
		reason, exempt := seqExemption(p.doc)
		stated := hasStopLaw(pkg, p)
		switch {
		case exempt && reason == "":
			out = append(out, p.report+": its "+seqExemptPrefix+" line gives no reason")
		case exempt && stated:
			out = append(out, p.report+" has the law "+law+" and a "+seqExemptPrefix+" line; remove the exemption")
		case !exempt && !stated:
			out = append(out, p.report+" returns a Seq and states no law "+law+"; add it, or a doc line \""+seqExemptPrefix+" <reason>\" with the reason no law can drive it")
		}
	}
	return out
}

// preludeSeq is the prelude's Seq type name.
func preludeSeq(t *testing.T, pkgs []stdLawPackage) *check.TypeName {
	t.Helper()
	for _, p := range pkgs {
		if p.path != "prelude" {
			continue
		}
		for _, tn := range p.pkg.Types {
			if tn.Name == "Seq" {
				return tn
			}
		}
	}
	t.Fatal("the prelude declares no Seq")
	return nil
}

// TestStdSeqProducersStateStop: each exported function and operation of
// the std packages and the prelude whose result is a Seq has its stop law
// or one seq-exempt line with a reason, never both.
func TestStdSeqProducersStateStop(t *testing.T) {
	pkgs := stdLawPackages(t)
	seq := preludeSeq(t, pkgs)
	seen := 0
	for _, p := range pkgs {
		seen += len(seqProducers(p.path, p.pkg, seq))
		for _, v := range seqRuleViolations(p.path, p.pkg, seq) {
			t.Error(v)
		}
	}
	// The walk finds the producers the checker declares; a walk that
	// finds none proves nothing. Today: slices.Values, maps.Keys,
	// maps.Values, strings.Lines, io.Lines twice, io.Stdin.Lines and
	// queue.Queue.Values.
	if seen < 8 {
		t.Errorf("the walk found %d Seq producers in std; want 8 or more", seen)
	}
}

// TestSeqStopRuleFixtures: the rule's walk finds a producer that no list
// names, in a package checked from source, and judges each case.
func TestSeqStopRuleFixtures(t *testing.T) {
	const src = `type Box { xs []int }
type Other { n int }

func Fresh(xs []int) Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

// seq-exempt: it reads the host.
func Exempt() Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

// seq-exempt:
func Blank() Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

func Lawful(xs []int) Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

law LawfulStops(xs []int) { true }

// seq-exempt: both.
func Both(xs []int) Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

law BothStops() { true }

func Items(b Box) Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

law BoxItemsStops(b Box) { true }

law ItemsStops() { true }

func Items(o Other) Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

func hidden() Seq[int] { Seq[int](func(yield func(v int) bool) {}) }

func Count(xs []int) int { len(xs) }

type Source {
	Next() Seq[int],
	// seq-exempt: the host.
	Host() Seq[int],
	Size() int,

	law NextStops() { true },
}

type Plain {
	Next() Seq[int],
	Size() int,

	law SizeIsStable() { Size() == Size() },
}
`
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("fixture", []*syntax.File{f}, nil)
	if len(ds) != 0 {
		t.Fatalf("fixture: %v", ds)
	}
	seq := preludeSeq(t, stdLawPackages(t))
	var names []string
	for _, p := range seqProducers("fixture", pkg, seq) {
		names = append(names, p.report)
	}
	if want := []string{"fixture.Fresh", "fixture.Exempt", "fixture.Blank", "fixture.Lawful", "fixture.Both", "fixture.Items(Box)", "fixture.Items(Other)", "fixture.Source.Next", "fixture.Source.Host", "fixture.Plain.Next"}; !slices.Equal(names, want) {
		t.Errorf("the producers are\n%q\nwant\n%q", names, want)
	}
	got := seqRuleViolations("fixture", pkg, seq)
	want := []string{
		`fixture.Fresh returns a Seq and states no law FreshStops; add it, or a doc line "// seq-exempt: <reason>" with the reason no law can drive it`,
		"fixture.Blank: its // seq-exempt: line gives no reason",
		"fixture.Both has the law BothStops and a // seq-exempt: line; remove the exemption",
		`fixture.Items(Other) returns a Seq and states no law OtherItemsStops; add it, or a doc line "// seq-exempt: <reason>" with the reason no law can drive it`,
		`fixture.Plain.Next returns a Seq and states no law Plain.NextStops; add it, or a doc line "// seq-exempt: <reason>" with the reason no law can drive it`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("the violations are\n%q\nwant\n%q", got, want)
	}
}
