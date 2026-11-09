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

// listedRuns writes the run of a law over every value (Listed laws), for
// the provider p of a member law or for nil, and reports whether it did:
// false when a value of the run cannot be listed, and the law's run takes
// generated cases (lawRuns).
func (l *lowerer) listedRuns(b *strings.Builder, law *check.Law, label string, p *check.LawProvider, scope *namer, env, failed string) bool {
	type value struct {
		name string
		t    check.Type
	}
	var vals []value
	if p != nil {
		vals = append(vals, value{"provider", p.Type})
	}
	for _, v := range law.Params {
		vals = append(vals, value{v.Name, v.Type})
	}
	types := make([]check.Type, len(vals))
	for i, v := range vals {
		types[i] = v.t
	}
	lists, cases, ok := l.enumRun(types, law.Decl)
	if !ok {
		return false
	}
	fmtPkg, run, conv := l.helper("fmt"), l.helper("Case"), l.helper("strconv")
	at := strconv.Quote(l.siteText(law.Decl))
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
		b.WriteString(ind + list + " := []" + (&typeCtx{l: l, site: law.Decl}).goType(v.t) + "{" + strings.Join(lists[i], ", ") + "}\n")
		b.WriteString(ind + "for _, " + a + " := range " + list + " {\n")
		ind += "\t"
		bound = append(bound, a)
		sep := ", "
		if i == 0 {
			sep = ": "
		}
		shown = append(shown, strconv.Quote(sep+v.name+" = "), l.litExpr(v.t, a, law.Decl))
	}
	e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
	args := []string{"&" + e}
	bind := ""
	if p != nil {
		fc := newFuncCtx(l, inner, false)
		fc.site = law.Decl
		own := law.Owner.Type.(*check.Named)
		bind = e + "." + l.envField(own.Obj) + " = " + fc.convert(p.Conv, bound[0], law.Decl) + "; "
		bound = bound[1:]
	}
	args = append(args, bound...)
	body := e + " := *" + env + "; " + bind + held + ", " + err + " := " + l.lawFunc(law) + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
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
