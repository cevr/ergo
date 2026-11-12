package lower

import (
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Cell writers (plan-2 §2, LR3). A writer operand of an operation type t
// that has one cell provider P (check.CellProvider: t is sealed in its own
// package, and every value of t is a cell of P) is a direct writer of P:
// the codec function is made for *P, the operand is the cell's own value
// (behind the cell's adapter), and each event is a call of P's function
// for the operation, not a Writer interface call. std json's Text is such
// a type: its provider is the Ergo record text.
//
// The events of one straight run (consecutive event lines of a codec
// function: no loop, branch or other call between them) are then a window
// of the rewrite engine (rewrite.go) with P's proved laws: each event
// unfolds by its definition (unfold.go), and the laws join the constant
// text, drop a comma that is not due and move setComma to the run's end.
// So a record's keys and commas are one constant append each. An event's
// arguments are the codec function's pure reads (a field, an element, a
// length or a constant), so dropping one, as an unfolded BeginObject(n)
// drops n, loses no evaluation.
//
// The evidence of a std provider's laws is the std law ledger (std
// ergo.laws); a law applies only when its entry is proved and current. A
// proved law holds for every instance, so its entry with no instance
// admits each site's constants. A provider of the program's own package
// takes the module ledger's evidence (Unit.Rewrite). The reference stays
// selectable: rewriteLowering "reference" writes the same calls of P's
// functions with no rewrite, and wireLowering "writer" takes the Writer
// interface.

// cellWriter is a direct writer by a cell: conv is the cell conversion of
// the provider prov into the operation type.
type cellWriter struct {
	conv check.Conversion
	prov *check.Named
}

// cellWriterOf is the cell writer of the operation type from, when from
// has one cell provider that provides every operation of the operation
// type to.
func cellWriterOf(from check.Type, to *check.Named) (cellWriter, bool) {
	conv, ok := check.CellProvider(from)
	if !ok {
		return cellWriter{}, false
	}
	iface, ok := to.Underlying().(*check.Interface)
	if !ok {
		return cellWriter{}, false
	}
	for _, op := range iface.Methods {
		if conv.Table[op.Name] == nil {
			return cellWriter{}, false
		}
	}
	src := conv.From
	if p, ok := src.(*check.Pointer); ok {
		src = p.Elem
	}
	prov, ok := src.(*check.Named)
	if !ok {
		return cellWriter{}, false
	}
	return cellWriter{conv: conv, prov: prov}, true
}

// cellOf is the cell writer of the direct path em, when it is one.
func (l *lowerer) cellOf(em wireEvents) (cellWriter, bool) {
	if em.via < 0 {
		return cellWriter{}, false
	}
	return cellWriterOf(l.directWriters[em.via], check.PreludeType(check.WireWriterName))
}

// directGoType is the Go type of direct writer w's codec functions: *P
// for a cell writer, the runtime type otherwise.
func (tc *typeCtx) directGoType(w check.Type) string {
	if cw, ok := cellWriterOf(w, check.PreludeType(check.WireWriterName)); ok {
		return "*" + tc.goType(cw.prov)
	}
	return tc.goType(w)
}

// cellOperand is the Go value of P behind the cell writer operand val: the
// cell's own value in its adapter (cells.go).
func (fc *funcCtx) cellOperand(cw cellWriter, val string) string {
	key, base, ok := fc.l.adapterName(cw.conv)
	if !ok {
		ice(nil, "the cell conversion into %s has no adapter", cw.conv.To)
	}
	name := fc.l.pkgName(pkgKey{"adapter", key}, base)
	return "&(" + val + ").(*" + name + ").v"
}

// cellEvents is text, Go statements of a codec function whose event lines
// are Writer method calls on w, with each straight run of events made
// calls of the cell provider's functions and rewritten by its laws.
func (l *lowerer) cellEvents(cw cellWriter, env, w, text string, site syntax.Node) string {
	task := taskOf(env)
	lines := strings.Split(text, "\n")
	var out []string
	var run []rwCall
	var texts [][]string
	flush := func() {
		if len(run) > 0 {
			out = append(out, l.cellRun(cw, env, w, run, texts, site)...)
		}
		run, texts = nil, nil
	}
	for _, line := range lines {
		op, arg, ok := eventLine(strings.TrimSpace(line), w, task)
		if !ok {
			flush()
			out = append(out, line)
			continue
		}
		fn := cw.conv.Table[op]
		if fn == nil {
			ice(site, "the event %s has no function of %s", op, cw.prov)
		}
		c := rwCall{fn: fn}
		var ts []string
		if arg != "" {
			c.args = append(c.args, rwArg{c: eventConst(arg)})
			ts = append(ts, arg)
		}
		run = append(run, c)
		texts = append(texts, ts)
	}
	flush()
	return strings.Join(out, "\n")
}

// eventLine reads an event line `w.Op(task)` or `w.Op(task, arg)`.
func eventLine(line, w, task string) (op, arg string, ok bool) {
	rest, ok := strings.CutPrefix(line, w+".")
	if !ok || !strings.HasSuffix(rest, ")") {
		return "", "", false
	}
	op, inner, ok := strings.Cut(strings.TrimSuffix(rest, ")"), "(")
	if !ok || op == "" {
		return "", "", false
	}
	if inner == task {
		return op, "", true
	}
	arg, ok = strings.CutPrefix(inner, task+", ")
	return op, arg, ok
}

// eventConst is the constant an event argument spells: a quoted string or
// a decimal integer; nil for any other expression.
func eventConst(arg string) constant.Value {
	if strings.HasPrefix(arg, "\"") {
		if s, err := strconv.Unquote(arg); err == nil {
			return constant.MakeString(s)
		}
		return nil
	}
	if n, err := strconv.ParseInt(arg, 10, 64); err == nil {
		return constant.MakeInt64(n)
	}
	return nil
}

// cellRun is the Go lines of one run of events: the rewritten calls when
// the provider's laws rewrite it, the run's own calls otherwise.
func (l *lowerer) cellRun(cw cellWriter, env, w string, run []rwCall, texts [][]string, site syntax.Node) []string {
	calls, rewritten := run, false
	if rw := l.cellRewriter(cw.prov); rw != nil {
		if got, changed := rw.run(cw.prov, run); changed {
			calls, rewritten = got, true
		}
	}
	var out []string
	for k, c := range calls {
		var args []string
		for j, a := range c.args {
			switch {
			case a.c != nil:
				args = append(args, l.constGo(c.fn.Sig.Params[j+1].Type, a.c, site))
			case !rewritten:
				args = append(args, texts[k][j])
			default:
				pos, arg := rwPlace(a.text)
				args = append(args, texts[pos][arg])
			}
		}
		self := w
		if !c.fn.Ptr {
			self = "(*" + w + ")"
		}
		out = append(out, l.wireCallFunc(c.fn, env, site, append([]string{self}, args...)...))
	}
	return out
}

// cellRewriter is the rewriter of the cell provider prov's laws: the run
// build's own for a provider of the root package, one on the std ledger
// for a std provider; nil when the build takes no evidence for it.
func (l *lowerer) cellRewriter(prov *check.Named) *rewriter {
	if rewriteLowering == "reference" || prov.Obj.Pkg == nil {
		return nil
	}
	if prov.Obj.Pkg == l.pkg {
		return l.rewrite
	}
	if !prov.Obj.Pkg.Std() {
		return nil
	}
	if l.cellRewrites == nil {
		l.cellRewrites = map[*check.Package]*rewriter{}
	}
	rw, ok := l.cellRewrites[prov.Obj.Pkg]
	if !ok {
		rw = newRewriter(prov.Obj.Pkg, newStdEvidence(prov.Obj.Pkg))
		l.cellRewrites[prov.Obj.Pkg] = rw
	}
	return rw
}

// stdEvidence is the std law ledger's evidence for a std package's laws.
type stdEvidence struct {
	entries map[string]LedgerEntry
	rules   map[string]rwRule
	digests map[string]string
}

// newStdEvidence reads the std ledger for the laws of pkg.
func newStdEvidence(pkg *check.Package) *stdEvidence {
	entries, err := ParseLedger(std.Ledger())
	if err != nil {
		ice(nil, "the std law ledger: %v", err)
	}
	e := &stdEvidence{entries: entries, rules: map[string]rwRule{}, digests: map[string]string{}}
	for prov, rules := range newRewriter(pkg, nil).rules {
		for _, r := range rules {
			e.rules[ledgerKey(LedgerNames(nil).law(r.law), LedgerNames(nil).provider(prov), "")] = r
		}
	}
	return e
}

// Admit admits a law whose std ledger entry is proved and current. A
// proved law holds at every instance, so in's constants are not looked up.
func (e *stdEvidence) Admit(law *check.Law, prov *check.Named, in RewriteInstance) (bool, string) {
	key := ledgerKey(LedgerNames(nil).law(law), LedgerNames(nil).provider(prov), "")
	entry, found := e.entries[key]
	if !found {
		return false, "the std law ledger has no " + key
	}
	d, ok := e.digests[key]
	if !ok {
		r := e.rules[key]
		d = LawDigest(law, prov, r.conv, r.ctor, "")
		e.digests[key] = d
	}
	if d != entry.Digest {
		return false, "the std law ledger's " + key + " is stale"
	}
	if entry.Verdict != "proved" {
		return false, "law " + key + " is " + entry.Verdict + ", not proved: only proved laws change code"
	}
	return true, ""
}
