package lower

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// lawContextRuns writes the runs of the law of lr under each named context
// that binds its owner (LawRun.Contexts; types.md, Laws 10): a skip line
// for one that does not run, else testCases cases, each of which copies
// the runner's environment, builds the context into the copy with its
// builder, as a with of it does, draws the law's parameters and calls the
// law's function, the provider being the one the context bound. The report
// names the context: `ok law T.L under Live (100 cases)`.
func (l *lowerer) lawContextRuns(b *strings.Builder, lr check.LawRun, scope *namer, env, seed, failed string) {
	law := lr.Law
	fmtPkg, run, conv := l.helper("fmt"), l.helper("Case"), l.helper("strconv")
	at := strconv.Quote(l.siteText(law.Decl))
	for _, lc := range lr.Contexts {
		label := "law " + l.lawTitle(law) + " under " + lc.Context.Name
		if lc.Unrun != "" {
			b.WriteString("\t" + fmtPkg + ".Println(" + strconv.Quote("skip "+label+": "+lc.Unrun) + ")\n")
			continue
		}
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
		b.WriteString("\t{\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(label) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		b.WriteString("\t\t\t" + r + ".size = " + i + " / 4\n")
		shown := []string{strconv.Quote("case "), conv + ".Itoa(" + i + ")", strconv.Quote(" of " + strconv.Itoa(testCases) + ", seed "), conv + ".FormatUint(" + seed + ", 10)", strconv.Quote(", size "), conv + ".Itoa(" + i + " / 4)"}
		sep := ": "
		cap, isNamed := lc.With.Discharged[0].(*check.Named)
		if !isNamed {
			ice(law.Decl, "a law run under %s whose owner is no capability", lc.Context.Name)
		}
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
		for k, v := range law.Params {
			vt := v.Type
			if lc.Params != nil {
				vt = lc.Params[k]
			}
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
		build := l.contextBuilder(lc.With, law.Decl)
		body := e + " := *" + env + "; " + build + "(&" + e + "); " +
			held + ", " + err + " := " + callee + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
		b.WriteString("\t\t\t" + ok + " = " + run + "(" + taskOf(env) + ", " + strconv.Quote(label) + ", " + at + ", func() string { return " + strings.Join(shown, " + ") + " }, func() error { " + body + " })\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\tif " + ok + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote(fmt.Sprintf("ok %s (%d cases)", label, testCases)) + ")\n")
		b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
	}
}
