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
// law's function, the provider being the one the context bound, and, when
// the build acquired a provider with a Close, releases the providers as the
// with would at the end of its block. A rewrite law has no law function:
// its run compares its two sides, each building the context into its own
// copy (contextRewriteSource; check does not run one whose build acquires
// a provider with a release). The report names the context: `ok law T.L under Live (100 cases)`. task is the
// runner's task, which lawRuns reads (taskOf).
func (l *lowerer) lawContextRuns(b *strings.Builder, lr check.LawRun, scope *namer, env, task, seed, failed string) {
	law := lr.Law
	fmtPkg := l.helper("fmt")
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
		// A run whose verdict ergo test found in its store (--cached;
		// lawcache) prints it, marked, and does not run.
		b.WriteString("\tif !" + l.helper("CachedRun") + "(" + strconv.Quote(label) + ") {\n")
		l.skippable(b, func() { l.lawContextRun(b, law, lc, label, scope, env, task, seed, failed, at) })
		b.WriteString("\t}\n")
	}
}

// lawContextRun writes the run of law under the named context lc,
// reported as label (lawContextRuns).
func (l *lowerer) lawContextRun(b *strings.Builder, law *check.Law, lc check.LawContext, label string, scope *namer, env, task, seed, failed, at string) {
	fmtPkg, conv := l.helper("fmt"), l.helper("strconv")
	cap, isNamed := lc.With.Discharged[0].(*check.Named)
	if !isNamed {
		ice(law.Decl, "a law run under %s whose owner is no capability", lc.Context.Name)
	}
	// body is a case's function body: it copies the runner's
	// environment, opens the case's folder for a context that needs
	// TestAmbient, builds the context and calls the law with vals. When
	// the build acquires a provider with a Close, the case owns it
	// (lawCaseScope) and releases it before the folder is removed.
	body := func(inner *namer, vals []string) string {
		e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
		args := []string{"&" + e}
		callee := l.lawFunc(law)
		if lawGenericOwner(law) != nil {
			// The law of a generic operation type takes its provider after
			// the environment, at the instance the context binds (lawDecl).
			args = append(args, e+"."+l.envField(cap))
			callee = l.lawCallee(law, lawGenericOwner(law), cap.TArgs) // mono.go
		}
		args = append(args, vals...)
		folder := ""
		if ta := lc.Context.TestAmbient; ta != nil {
			// The case's own folder, removed when the case returns
			// (rows.md, Test's ambient).
			folder = testAmbientOpen(l, e, inner.alloc("dir"), ta) + "; "
		}
		build := l.contextBuilder(lc.With, law.Decl)
		call := held + ", " + err + " := " + callee + "(" + strings.Join(args, ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
		if ownsResources(lc.With) {
			head, open := l.lawCaseScope(inner, e, env, task, build, folder)
			return "return " + head + open + call + " }()"
		}
		return e + " := *" + env + "; " + folder + build + "(&" + e + "); " + call
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
		return
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

// lawCaseScope is the head and opening of a law case whose context build
// acquires a provider with a Close. The case owns the providers it builds,
// as a with does (resources.md, Context ownership): the owning builder
// registers their releases in the with scope ws, which the case's frame
// scope fs ends when the case ends, on every path, after the law's last
// operation; last built first, each once. A release's defect is the
// case's exit, so it fails the case ($Prop). e is the case's environment,
// a copy of env; folder opens the case's folder in e (testAmbientOpen) or
// is empty, and its removal, deferred first, runs after the releases;
// build is the context's owning builder.
func (l *lowerer) lawCaseScope(inner *namer, e, env, task, build, folder string) (head, open string) {
	fs, ws, exit := inner.alloc("fs"), inner.alloc("ws"), inner.alloc("exit")
	head = "func() (" + exit + " error) { "
	open = e + " := *" + env + "; " + folder + fs + " := " + l.rt() + ".NewScope(" + task + "); defer " + fs + ".End(&" + exit + "); " +
		ws + " := " + fs + ".OpenWith(); " + build + "(&" + e + ", " + env + ", " + ws + "); "
	return head, open
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
