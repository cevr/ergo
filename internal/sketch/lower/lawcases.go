package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Listed laws (MU6b; types.md, Laws 10). One rule decides how every law
// with a body runs, a top-level law, a member law of an operation type and
// a law in a machine alike: when the values of its run can all be listed
// (enumValues: bool, records and unions of such, a type defined over one,
// tuples, at most enumCap values a type, and at most enumCases cases in
// all), the run takes every value once and a run that holds is reported
// proved. A member law's run lists its provider's values as well as its
// parameters, since the law is quantified over both. A run with a value
// that cannot be listed takes 100 generated cases and is reported tested,
// never proved (NORTH_STAR pillar 4). A law with no parameters, at top
// level or in a machine, has one case, so it is proved by its one run.
// A property test (a test with parameters) follows the same rule over its
// parameters (listedTest; mu6c, owner q39); a test with no parameters is
// an example and runs once.
//
// Prior art: SmallCheck and Lean's `decide` check a proposition over
// every value of a small finite type; QuickCheck and Hypothesis generate
// cases when the type is large, and report only that the cases passed.

// enumCap bounds the values of one listed type, and enumCases the cases of
// a listed run.
const (
	enumCap   = 256
	enumCases = 4096
)

// enumValues is every value of t as a Go expression, when t can be listed
// with at most enumCap values: bool, a record of such fields, a union of
// such variants, a type defined over one, and a tuple of such.
func (l *lowerer) enumValues(t check.Type, site syntax.Node, seen map[*check.Named]bool) ([]string, bool) {
	tc := &typeCtx{l: l, site: site}
	product := func(goType string, names []string, types []check.Type) ([]string, bool) {
		out := []string{""}
		for i, ft := range types {
			vs, ok := l.enumValues(ft, site, seen)
			if !ok || len(out)*len(vs) > enumCap {
				return nil, false
			}
			var next []string
			for _, prefix := range out {
				for _, v := range vs {
					sep := ""
					if prefix != "" {
						sep = ", "
					}
					next = append(next, prefix+sep+names[i]+": "+v)
				}
			}
			out = next
		}
		for i := range out {
			out[i] = goType + "{" + out[i] + "}"
		}
		return out, true
	}
	switch u := t.(type) {
	case *check.Basic:
		if u.Kind == check.Bool {
			return []string{"false", "true"}, true
		}
	case *check.Tuple:
		names := make([]string, len(u.Types))
		for i := range u.Types {
			names[i] = tupleField(i)
		}
		return product(tc.goType(u), names, u.Types)
	case *check.Named:
		if seen[u] {
			return nil, false // a recursive type has no finite list
		}
		seen[u] = true
		defer delete(seen, u)
		switch under := u.Underlying().(type) {
		case *check.Struct:
			td := l.typeDecl(u.Obj, site)
			names := make([]string, len(under.Fields))
			types := make([]check.Type, len(under.Fields))
			for i, f := range under.Fields {
				names[i], types[i] = td.fieldName[f.Name], f.Type
			}
			return product(tc.goType(u), names, types)
		case *check.Union:
			var out []string
			for _, v := range under.Variants {
				vs, ok := l.enumValues(v, site, seen)
				if !ok || len(out)+len(vs) > enumCap {
					return nil, false
				}
				out = append(out, vs...)
			}
			return out, true
		case *check.Basic:
			vs, ok := l.enumValues(under, site, seen)
			for i := range vs {
				vs[i] = tc.goType(u) + "(" + vs[i] + ")"
			}
			return vs, ok
		}
	}
	return nil, false
}

// enumRun lists every value of each type of a run, in order, when each
// can be listed and their cases number at most enumCases.
func (l *lowerer) enumRun(types []check.Type, site syntax.Node) ([][]string, int, bool) {
	n := 1
	var all [][]string
	for _, t := range types {
		vs, ok := l.enumValues(t, site, map[*check.Named]bool{})
		if !ok || n*len(vs) > enumCases {
			return nil, 0, false
		}
		n *= len(vs)
		all = append(all, vs)
	}
	return all, n, true
}

// provedText is the report of a listed run that holds.
func provedText(cases int) string {
	if cases == 1 {
		return "proved: 1 case"
	}
	return "proved: all " + strconv.Itoa(cases) + " cases"
}

// listedValue is one value of a listed run: its name in the report and
// its type.
type listedValue struct {
	name string
	t    check.Type
}

// listedRuns writes the run of a law over every value (Listed laws), for
// the binding bd of its slots (check.Law.Slots: the owner's provider, and
// each parameter of an operation type), and reports whether it did: false
// when a value of the run cannot be listed, or a provider is built by its
// constructor, and the law's run takes generated cases (lawRuns).
func (l *lowerer) listedRuns(b *strings.Builder, law *check.Law, label string, bd []check.LawProvider, scope *namer, env, failed string) bool {
	owner := -1
	bound := map[int]int{} // a parameter's index -> its slot's index
	for k, s := range law.Slots {
		if bd[k].Ctor != nil {
			return false
		}
		if s.Param < 0 {
			owner = k
		} else {
			bound[s.Param] = k
		}
	}
	var vals []listedValue
	if owner >= 0 {
		vals = append(vals, listedValue{"provider", bd[owner].Type})
	}
	for i, v := range law.Params {
		if k, ok := bound[i]; ok {
			vals = append(vals, listedValue{v.Name, bd[k].Type})
			continue
		}
		vals = append(vals, listedValue{v.Name, v.Type})
	}
	at := strconv.Quote(l.siteText(law.Decl))
	return l.listedRun(b, label, law.Decl, vals, scope, env, failed, func(inner *namer, vs []string) string {
		e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
		fc := newFuncCtx(l, inner, false)
		fc.site = law.Decl
		bind := ""
		if owner >= 0 {
			bind = e + "." + l.envField(law.Slots[owner].Type.Obj) + " = " + fc.convert(bd[owner].Conv, vs[0], law.Decl) + "; "
			vs = vs[1:]
		}
		args := []string{"&" + e}
		for i := range law.Params {
			if k, ok := bound[i]; ok {
				args = append(args, fc.convert(bd[k].Conv, vs[i], law.Decl))
				continue
			}
			args = append(args, vs[i])
		}
		return e + " := *" + env + "; " + bind + held + ", " + err + " := " + l.lawFunc(law) + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
	})
}

// listedTest writes the run of a property test over every value of its
// parameters, by the rule of laws (Listed laws; runtime.md, Tests 3), and
// reports whether it did: false for an example test, which has no
// parameters and runs once, and when a parameter cannot be listed, and
// the property takes generated cases (testRunner).
func (l *lowerer) listedTest(b *strings.Builder, t *check.Test, scope *namer, env, failed string) bool {
	if len(t.Params) == 0 {
		return false
	}
	vals := make([]listedValue, len(t.Params))
	for i, v := range t.Params {
		vals[i] = listedValue{v.Name, v.Type}
	}
	return l.listedRun(b, t.Name, t.Decl, vals, scope, env, failed, func(_ *namer, bound []string) string {
		return "return " + l.testFunc(t) + "(" + strings.Join(append([]string{env}, bound...), ", ") + ")"
	})
}

// listedRun writes a run over every combination of the values vals, in
// order, the last one fastest, when each can be listed and the cases
// number at most enumCases, and reports whether it did. call is the body
// of a case's function, given the case's namer and the bound values. The
// run prints `ok label (proved: …)` when every case holds, and stops at
// its first case that fails.
func (l *lowerer) listedRun(b *strings.Builder, label string, site syntax.Node, vals []listedValue, scope *namer, env, failed string, call func(inner *namer, bound []string) string) bool {
	types := make([]check.Type, len(vals))
	for i, v := range vals {
		types[i] = v.t
	}
	lists, cases, ok := l.enumRun(types, site)
	if !ok {
		return false
	}
	fmtPkg, run, conv := l.helper("fmt"), l.helper("Case"), l.helper("strconv")
	at := strconv.Quote(l.siteText(site))
	inner := newNamer(scope)
	// k counts the cases, for the report of a refuted one; a run with no
	// values has one case and no count.
	ok1, k := inner.alloc("ok"), inner.alloc("k")
	if len(vals) > 0 {
		b.WriteString("\t{\n\t\t" + ok1 + ", " + k + " := true, 0\n")
	} else {
		b.WriteString("\t{\n\t\t" + ok1 + " := true\n")
	}
	shown := []string{strconv.Quote("case "), conv + ".Itoa(" + k + ")", strconv.Quote(" of " + strconv.Itoa(cases) + ", every value")}
	ind := "\t\t"
	var bound []string
	for i, v := range vals {
		base := v.name
		if base == "_" {
			base = "arg"
		}
		list, a := inner.alloc(base+"s"), inner.alloc(base)
		b.WriteString(ind + list + " := []" + (&typeCtx{l: l, site: site}).goType(v.t) + "{" + strings.Join(lists[i], ", ") + "}\n")
		b.WriteString(ind + "for _, " + a + " := range " + list + " {\n")
		ind += "\t"
		bound = append(bound, a)
		sep := ", "
		if i == 0 {
			sep = ": "
		}
		shown = append(shown, strconv.Quote(sep+v.name+" = "), l.litExpr(v.t, a, site))
	}
	body := call(inner, bound)
	// The first refuted case ends the run; the cases after it are skipped.
	b.WriteString(ind + "if " + ok1 + " {\n")
	describe := "nil"
	if len(vals) > 0 {
		b.WriteString(ind + "\t" + k + "++\n")
		describe = "func() string { return " + strings.Join(shown, " + ") + " }"
	}
	b.WriteString(ind + "\t" + ok1 + " = " + run + "(" + taskOf(env) + ", " + strconv.Quote(label) + ", " + at + ", " + describe + ", func() error { " + body + " })\n")
	b.WriteString(ind + "}\n")
	for range vals {
		ind = ind[:len(ind)-1]
		b.WriteString(ind + "}\n")
	}
	b.WriteString("\t\tif " + ok1 + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+label+" ("+provedText(cases)+")") + ")\n")
	b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
	return true
}
