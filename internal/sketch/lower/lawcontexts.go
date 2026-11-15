package lower

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// lawContextRuns writes the runs of the law of lr under each named context
// that binds its owner (LawRun.Contexts; types.md, Laws 10): a skip line
// for one that does not run, else its cases by the one rule (one case
// for a law with no parameters, every value when they can all be listed,
// testCases cases otherwise), each of which copies
// the runner's environment, builds the context into the copy with its
// builder, as a with of it does, draws the law's parameters and calls the
// law's function, the provider being the one the context bound. The report
// names the context: `ok law T.L under Live (100 cases)`. task is the
// runner's task, which lawRuns reads (taskOf).
func (l *lowerer) lawContextRuns(b *strings.Builder, lr check.LawRun, scope *namer, env, task, seed, failed string) {
	law := lr.Law
	fmtPkg, conv := l.helper("fmt"), l.helper("strconv")
	at := strconv.Quote(l.siteText(law.Decl))
	for _, lc := range lr.Contexts {
		label := "law " + l.lawTitle(law) + " under " + lc.Context.Name
		if lc.Unrun != "" {
			b.WriteString("\t" + fmtPkg + ".Println(" + strconv.Quote("skip "+label+": "+lc.Unrun) + ")\n")
			b.WriteString("\t" + l.helper("Record") + "(" + strconv.Quote(label) + ", nil, " + strconv.Quote(lc.Unrun) + ")\n")
			continue
		}
		cap, isNamed := lc.With.Discharged[0].(*check.Named)
		if !isNamed {
			ice(law.Decl, "a law run under %s whose owner is no capability", lc.Context.Name)
		}
		// body is a case's function body: it copies the runner's
		// environment, opens the case's folder for a context that needs
		// TestAmbient, builds the context and calls the law with vals.
		body := func(inner *namer, vals []string) string {
			e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
			args := []string{"&" + e}
			callee := l.lawFunc(law)
			if lawGenericOwner(law) != nil {
				// The law of a generic operation type takes its provider after
				// the environment, at the instance the context binds (lawDecl).
				args = append(args, e+"."+l.envField(cap))
				tc := &typeCtx{l: l, site: law.Decl}
				targs := make([]string, len(cap.TArgs))
				for k, a := range cap.TArgs {
					targs[k] = tc.goType(a)
				}
				callee += "[" + strings.Join(targs, ", ") + "]"
			}
			args = append(args, vals...)
			open := ""
			if ta := lc.Context.TestAmbient; ta != nil {
				// The case's own folder, removed when the case returns
				// (rows.md, Test's ambient).
				open = testAmbientOpen(l, e, inner.alloc("dir"), ta) + "; "
			}
			return e + " := *" + env + "; " + open + l.contextBuilder(lc.With, law.Decl) + "(&" + e + "); " +
				held + ", " + err + " := " + callee + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
		}
		// The one rule (types.md, Laws 10): the context builds its provider,
		// so a run's values are the law's parameters alone. A law with none
		// has one case, and one whose parameters can all be listed takes
		// every value; only the others draw testCases cases.
		vals := make([]listedValue, len(law.Params))
		for k, v := range law.Params {
			vals[k] = listedValue{v.Name, v.Type}
			if lc.Params != nil {
				vals[k].t = lc.Params[k]
			}
		}
		if l.listedRun(b, label, law.Decl, vals, true, scope, env, failed, l.replayLine(law, `""`), body) {
			continue
		}
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(label) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		b.WriteString("\t\t\t" + r + ".start(" + i + ")\n")
		b.WriteString("\t\t\t" + ok + " = " + l.helper("Prop") + "(" + task + ", " + strconv.Quote(label) + ", " + at + ", " + r + ", func() (func() string, func() error) {\n")
		shown := []string{strconv.Quote("case "), conv + ".Itoa(" + i + ")", strconv.Quote(" of " + strconv.Itoa(testCases) + ", seed "), conv + ".FormatUint(" + seed + ", 10)", strconv.Quote(", size "), conv + ".Itoa(" + i + " / 4)"}
		sep := ": "
		var args []string
		for k, v := range law.Params {
			vt := vals[k].t
			base := v.Name
			if base == "_" {
				base = "arg"
			}
			a := inner.alloc(base)
			b.WriteString("\t\t\t" + a + " := " + l.genExpr(vt, r, strconv.Itoa(testDepth), inner, law.Decl) + "\n")
			args = append(args, a)
			shown = append(shown, strconv.Quote(sep+v.Name+" = "), l.litExpr(vt, a, law.Decl))
			sep = ", "
		}
		b.WriteString("\t\t\treturn func() string { return " + strings.Join(shown, " + ") + " }, func() error { " + body(inner, args) + " }\n\t\t\t})\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\tif " + ok + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote(fmt.Sprintf("ok %s (%d cases)", label, testCases)) + ")\n")
		b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n")
		b.WriteString("\t\t\t" + l.replayLine(law, strconv.Quote("--seed ")+" + "+conv+".FormatUint("+seed+", 10)") + "\n\t\t}\n")
		b.WriteString("\t\t" + l.helper("Record") + "(" + strconv.Quote(label) + ", " + r + ", \"\")\n\t}\n")
	}
}
