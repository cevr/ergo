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
// law's function, the provider being the one the context bound. A rewrite
// law has no law function: its run compares its two sides, each building
// the context into its own copy (contextRewriteSource). The report
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
		if law.Rewrite != nil {
			// A rewrite law compares its two sides, each on a fresh
			// provider the context's builder makes, as rewriteRuns does
			// with a constructor; it has no law function to call.
			b.WriteString("\tif !" + l.helper("CachedRun") + "(" + strconv.Quote(label) + ") {\n")
			l.rewriteCompare(b, law, label, l.contextRewriteSource(law, lc), scope, env, seed, failed, at)
			b.WriteString("\t}\n")
			continue
		}
		inner := newNamer(scope)
		r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
		e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
		// A run whose verdict ergo test found in its store (--cached;
		// lawcache) prints it, marked, and does not run.
		b.WriteString("\tif !" + l.helper("CachedRun") + "(" + strconv.Quote(label) + ") {\n")
		b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(label) + ")\n")
		b.WriteString("\t\t" + r + ".env = " + env + "\n")
		b.WriteString("\t\t" + ok + " := true\n")
		b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(testCases) + "; " + i + "++ {\n")
		b.WriteString("\t\t\t" + r + ".start(" + i + ")\n")
		b.WriteString("\t\t\t" + ok + " = " + l.helper("Prop") + "(" + task + ", " + strconv.Quote(label) + ", " + at + ", " + r + ", func() (func() string, func() error) {\n")
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
		b.WriteString("\t\t\treturn func() string { return " + strings.Join(shown, " + ") + " }, func() error { " + body + " }\n\t\t\t})\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\tif " + ok + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote(fmt.Sprintf("ok %s (%d cases)", label, testCases)) + ")\n")
		b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n")
		b.WriteString("\t\t\t" + l.replayLine(law, strconv.Quote("--seed ")+" + "+conv+".FormatUint("+seed+", 10)") + "\n\t\t}\n")
		b.WriteString("\t\t" + l.helper("Record") + "(" + strconv.Quote(label) + ", " + r + ", \"\")\n\t}\n")
	}
}

// contextRewriteSource is where the cases of the rewrite law law's run
// under the named context lc take their providers (rewriteCompare): each
// side builds lc's context into its copy of the environment, which binds
// the owner's provider; a case draws nothing for it. A rewrite law of a
// generic operation type does not run (check's laws.go), so the owner is
// the capability the context binds.
func (l *lowerer) contextRewriteSource(law *check.Law, lc check.LawContext) rewriteSource {
	build := l.contextBuilder(lc.With, law.Decl)
	return rewriteSource{
		ops: lc.With.Discharged[0],
		draw: func(func(string), *namer, string, string) []string {
			return []string{strconv.Quote(": provider from context " + lc.Context.Name)}
		},
		bind: func(w func(string), _ *funcCtx, _ *namer, e, _ string) {
			w("\t" + build + "(&" + e + ")")
		},
	}
}
