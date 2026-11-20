package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// Law parameters found by search (CONSTRAINTS C5; runtime.md, Tests 6). A
// law parameter whose where names an earlier parameter or an operation of
// the law's type has an admission law (check.Law.Admit), lowered as a law
// is (lawDecl), over the parameters up to it. The run draws the
// parameter with testgen.Search: among the near variants of the earlier
// parameter of its type that the where names (check.Law.Near), the first
// the admission accepts and that is not identical to it. A case some where
// does not admit is not run. The run counts the admitted cases whose
// searched values differ from their sources, and one with none is a skip,
// never a pass: its ties were all identical, which say nothing about the
// law.

// admissionOf is the admission law of law's parameter k, or nil.
func admissionOf(law *check.Law, k int) *check.Law {
	if k < len(law.Admit) {
		return law.Admit[k]
	}
	return nil
}

// nearDraw writes the case's draw of parameter k, named a, of type t: the
// search over its source's near variants, with gen as its fresh draw and
// adm as its where. args are the law call's arguments so far (the
// environment, then any provider and witnesses, then the parameters
// before k), bind the statements that bind the case's providers on an
// environment named e. It folds the draw's flags into admitted and
// distinct.
func (l *lowerer) nearDraw(b *strings.Builder, law, adm *check.Law, k int, a string, t check.Type, gen string, args, bind []string, lawCallee, e, env, r, admitted, distinct string, inner *namer, drawn []string, offset int) {
	tc := &typeCtx{l: l, site: law.Decl}
	gt := tc.goType(t)
	src, hasSrc := "*new("+gt+")", "false"
	if k < len(law.Near) && law.Near[k] >= 0 && law.Near[k]+offset < len(drawn) && drawn[law.Near[k]+offset] != "" {
		src, hasSrc = drawn[law.Near[k]+offset], "true"
	}
	ea, x, h, err := inner.alloc("ea"), inner.alloc("x"), inner.alloc("h"), inner.alloc("err")
	var binds []string
	for _, s := range bind {
		binds = append(binds, strings.Replace(s, e+".", ea+".", 1))
	}
	// The law's own call's type arguments, a generic owner's instance.
	callee := l.lawFunc(adm) + strings.TrimPrefix(lawCallee, l.lawFunc(law))
	call := append([]string{"&" + ea}, args[1:]...)
	call = append(call, x)
	ok, dist := inner.alloc("admits"), inner.alloc("near")
	b.WriteString("\t\t\t" + a + ", " + ok + ", " + dist + " := " + l.helper("testgen") + ".Search(" + r + ".stream, " + src + ", " + hasSrc +
		", func() " + gt + " { return " + gen + " }, func(" + x + " " + gt + ") bool { " + ea + " := *" + env + "; " +
		strings.Join(append(binds, ""), "; ") + h + ", " + err + " := " + callee + "(" + strings.Join(call, ", ") + "); return " + err + " == nil && " + h + " })\n")
	b.WriteString("\t\t\t" + admitted + ", " + distinct + " = " + admitted + " && " + ok + ", " + distinct + " || " + dist + "\n")
}

// nearReport writes the start of the run's verdict chain: a run that held
// and admitted no case with distinct searched values is a skip, with how
// to reach such values; the ok and failure branches follow it.
func (l *lowerer) nearReport(b *strings.Builder, law *check.Law, label string, cases int, r, ok string) {
	why := "no admitted case had a searched value distinct from its source within the near variants (" + strconv.Itoa(cases) +
		" cases); reach such values with a generator (func New…) or a law that names them"
	b.WriteString("\t\tif " + ok + " && " + r + ".distinct == 0 {\n")
	b.WriteString("\t\t\t" + l.helper("fmt") + ".Println(" + strconv.Quote("skip "+label+": "+why) + ")\n")
	b.WriteString("\t\t\t" + r + ".skip = " + strconv.Quote(why) + "\n")
	b.WriteString("\t\t} else ")
}
