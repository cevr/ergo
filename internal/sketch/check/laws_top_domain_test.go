package check_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

// diagKeys are a program's diagnostics as (code, message, line offset
// within the law): a line before the law is "pre" at its absolute line,
// identical in both placements since the helpers are a common prefix.
func diagKeys(diags []check.Diagnostic, lawLine int) []string {
	var out []string
	for _, d := range diags {
		where := fmt.Sprintf("law+%d", d.Line-lawLine)
		if d.Line < lawLine {
			where = fmt.Sprintf("pre %d", d.Line)
		}
		out = append(out, fmt.Sprintf("%s %s: %s", where, d.Code, d.Message))
	}
	sort.Strings(out)
	return out
}

// lawFacts lists the facts Info records at each node of the law L, in walk
// order: its owner, its type, the object an identifier uses, a call's kind
// and a statement's tail type. Objects are named by kind and name, since
// the two programs are distinct packages.
func lawFacts(pkg *check.Package) (facts []string, fails string) {
	var law *check.Law
	for _, l := range pkg.Laws {
		if l.Name == "L" {
			law = l
		}
	}
	if law == nil {
		return []string{"no law L"}, ""
	}
	info := pkg.Info
	syntax.Inspect(law.Decl.Body, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		f := fmt.Sprintf("%T owner=%v", n, pkg.OwnerOf(n))
		if x, ok := n.(syntax.Expr); ok {
			if t := info.Types[x]; t != nil {
				f += " type=" + t.String()
			}
		}
		if id, ok := n.(*syntax.Ident); ok {
			switch o := info.Uses[id].(type) {
			case nil:
			case *check.Var:
				f += " use=var " + o.Name
			case *check.Func:
				f += " use=func " + o.Name
			case *check.TypeName:
				f += " use=type " + o.Name
			case *check.Method:
				f += " use=method " + o.Name
			default:
				f += fmt.Sprintf(" use=%T", o)
			}
		}
		if x, ok := n.(*syntax.CallExpr); ok {
			if c := info.Calls[x]; c != nil {
				f += fmt.Sprintf(" call=%v", c.Kind)
			}
		}
		if s, ok := n.(syntax.Stmt); ok {
			if t := info.Tails[s]; t != nil {
				f += " tail=" + t.String()
			}
		}
		facts = append(facts, f)
		return true
	})
	var elems []string
	for _, t := range law.Fails.Elems() {
		elems = append(elems, t.String())
	}
	sort.Strings(elems)
	return facts, strings.Join(elems, "|")
}

// ruleEight counts the tail rule's diagnostics (types.md Laws 8).
func ruleEight(diags []check.Diagnostic) int {
	n := 0
	for _, d := range diags {
		if strings.HasPrefix(d.Message, "a law's body ends in a bool") {
			n++
		}
	}
	return n
}

// TestTopLevelLawMatchesMember: spec A3. A top-level law is checked as a
// member law is, minus the operation scope: for every generated body
// (sketchtest.GenLaws, a seeded grammar of law-body statements), the two
// placements give the same diagnostics and, when clean, the same failure
// row and the same facts at every node of the body. A second oracle, the
// generator's own kinds, decides the tail rule.
func TestTopLevelLawMatchesMember(t *testing.T) {
	laws := sketchtest.GenLaws(sketchtest.LawDomainSeed, sketchtest.LawDomainSize)
	if len(laws) < 2000 {
		t.Fatalf("%d programs, want at least 2000", len(laws))
	}
	clean, refused, tailRefused := 0, 0, 0
	for i, l := range laws {
		top, topLine := sketchtest.PlaceLaw(false, l)
		mem, memLine := sketchtest.PlaceLaw(true, l)
		topPkg, topDiags := check.CheckSource(top)
		memPkg, memDiags := check.CheckSource(mem)
		law := "law L(" + l.Params + ") {\n" + strings.Join(l.Body, "\n") + "\n}"
		tk, mk := diagKeys(topDiags, topLine), diagKeys(memDiags, memLine)
		if strings.Join(tk, "\n") != strings.Join(mk, "\n") {
			t.Fatalf("program %d: the placements differ\ntop-level:\n\t%s\nmember:\n\t%s\n%s", i, strings.Join(tk, "\n\t"), strings.Join(mk, "\n\t"), law)
		}
		want := 0
		for _, k := range l.Exits {
			if !k.Boolish() {
				want++
			}
		}
		if got := ruleEight(topDiags); got != want {
			t.Fatalf("program %d: %d tail-rule diagnostics, the generator's kinds say %d\n\t%s\n%s", i, got, want, describe(topDiags), law)
		}
		if want > 0 {
			tailRefused++
		}
		if len(topDiags) > 0 {
			refused++
			continue
		}
		clean++
		tf, tfails := lawFacts(topPkg)
		mf, mfails := lawFacts(memPkg)
		if tfails != mfails {
			t.Fatalf("program %d: failure rows differ: top-level %q, member %q\n%s", i, tfails, mfails, law)
		}
		if strings.Join(tf, "\n") != strings.Join(mf, "\n") {
			t.Fatalf("program %d: facts differ\ntop-level:\n\t%s\nmember:\n\t%s\n%s", i, strings.Join(tf, "\n\t"), strings.Join(mf, "\n\t"), law)
		}
	}
	// The domain is not degenerate: both verdicts and the tail rule occur.
	if clean < len(laws)/10 || refused < len(laws)/10 || tailRefused < len(laws)/20 {
		t.Fatalf("degenerate domain: %d clean, %d refused, %d refused by the tail rule of %d", clean, refused, tailRefused, len(laws))
	}
	t.Logf("%d programs: %d clean, %d refused (%d by the tail rule)", len(laws), clean, refused, tailRefused)
}
