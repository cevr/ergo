package lower

import (
	"go/constant"
	"go/token"
	"regexp"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
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
// cellFuncs: a cell writer's codec function takes P's value and gives it
// back, func(env, w P, v T) P, as Go's append passes a slice, and works on
// &w inside; a call of another codec function, or of a codec witness's
// field, is `*w = f(env, *w, x)`. So no pointer to the cell reaches a
// function value, and Go keeps the caller's cell on its stack: the encode
// makes no allocation for its writer. (Measured: a *P parameter instead
// costs more instructions per encode, since each append then goes
// through memory the callee may alias.)
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

// directGoType is the Go type of direct writer w's codec functions: P for
// a cell writer (its state passes by value, cellFuncs), the runtime type
// otherwise.
func (tc *typeCtx) directGoType(w check.Type) string {
	if cw, ok := cellWriterOf(w, check.PreludeType(check.WireWriterName)); ok {
		return tc.goType(cw.prov)
	}
	return tc.goType(w)
}

// cellOperand is the Go value of P behind the cell writer operand x, whose
// Go text is val: the cell's own value in its adapter (cells.go).
func (fc *funcCtx) cellOperand(cw cellWriter, x syntax.Expr, val string) string {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		if v, ok := fc.l.info.Uses[id].(*check.Var); ok {
			if _, recv, ok := fc.cellWindow(v); ok {
				return recv
			}
		}
	}
	key, base, ok := fc.l.adapterName(cw.conv)
	if !ok {
		ice(nil, "the cell conversion into %s has no adapter", cw.conv.To)
	}
	name := fc.l.pkgName(pkgKey{"adapter", key}, base)
	return "&(" + val + ").(*" + name + ").v"
}

// cellOpCall lowers x, an operation call on a cell local declared by its
// adapter's Go type (noteCell), as the call of the provider's function on
// the cell's own value: the adapter's method makes that call (with an
// environment of the calling task), so the value is the same and the call
// is static. ok is false for any other call, and for a function that fails,
// needs a service or takes a function value, whose adapter method adapts
// more than the receiver.
func (fc *funcCtx) cellOpCall(x *syntax.CallExpr, c *check.Call, args []syntax.Expr) (text string, result, ok bool) {
	id, isID := unparen(c.Recv).(*syntax.Ident)
	if !isID || c.Op == nil {
		return "", false, false
	}
	v, isVar := fc.l.info.Uses[id].(*check.Var)
	if !isVar || !fc.cellConcrete[v] {
		return "", false, false
	}
	fn := fc.cells[v].Table[c.Op.Name]
	if fn == nil || fc.l.useFunc(fn, x).fallible {
		return "", false, false
	}
	for _, p := range fn.Sig.Params {
		if _, isFunc := p.Type.(*check.Signature); isFunc {
			return "", false, false
		}
	}
	self := "&" + fc.locals[v] + ".v"
	if !fn.Ptr {
		self = fc.locals[v] + ".v"
	}
	vals := fc.callArgs(args, x)
	return fc.l.wireCallFunc(fn, fc.envExpr(x), x, append([]string{self}, vals...)...), len(fn.Sig.Results) > 0, true
}

// cellEvents is text, Go statements of a codec function whose event lines
// are Writer method calls on w, with each straight run of events made
// calls of the cell provider's functions and rewritten by its laws.
//
// Carry: the provider's state where a run ends is known when the run's
// last call is constant and idempotent by the laws (c; c == c, as
// SetCommaLast proves for setComma). An `if` arm that follows the run
// directly, behind a condition that names no w, starts in that state, so
// its run is rewritten as c then the arm: since the state is c's own,
// running c once more changes nothing. After the arm's closing brace the
// state is still c's when the arm also ends with c (no else: either path
// ends in it).
//
// The carry follows the text's blocks: an `if` or a `switch` whose header
// names no w opens a block that starts in the state before it; each arm
// (the then arm, `} else {`, a `case` or `default:`) starts there; an
// `if` with no else ends in that state when its arm does. A `for` body,
// and any statement that names w, leaves the state unknown; a statement
// that names no w keeps it. A line with a brace the reader does not know
// stops the carry for the rest of the text. start is the state the text
// starts in (a codec function's entry variant, cellVariant), or nil.
//
// A call of a cell codec function where the state is known calls its
// variant for that state: the variant's runs know their entry.
func (l *lowerer) cellEvents(cw cellWriter, env, w, text string, site syntax.Node) string {
	return l.cellEventsFrom(cw, env, w, text, nil, site)
}

// cellBlock is a block of the text cellEvents reads: the state its arms
// start in (nil when unknown), whether its arms are a switch's, and
// whether it is an if with no else so far.
type cellBlock struct {
	entry       *rwCall
	cases, bare bool
}

// cellEventsFrom is cellEvents of a text that starts in start's state.
func (l *lowerer) cellEventsFrom(cw cellWriter, env, w, text string, start *rwCall, site syntax.Node) string {
	task := taskOf(env)
	lines := strings.Split(text, "\n")
	names := regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`)
	var out []string
	var run []rwCall
	var texts [][]string
	var stack []cellBlock
	// tail is the known state's call where the text now stands.
	tail := start
	stopped := false
	// lead is 1 when the run starts with the state's call, carried (not
	// written by the text).
	lead := 0
	begin := func(c *rwCall) {
		tail = c
		if stopped {
			tail = nil
		}
	}
	flush := func() {
		if len(run) > 0 {
			lines, last := l.cellRun(cw, env, w, run, texts, lead, site)
			out = append(out, lines...)
			tail = l.idempotent(cw.prov, last)
		}
		run, texts, lead = nil, nil, 0
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // no statement: the run goes on
		}
		op, arg, ok := eventLine(trimmed, w, task)
		if !ok {
			flush()
			if tail != nil && !stopped {
				line = l.cellEntryCall(line, w, *tail)
			}
			out = append(out, line)
			mentions := names.MatchString(trimmed)
			opens := strings.HasSuffix(trimmed, " {")
			switch {
			case stopped:
				tail = nil
			case opens && (strings.HasPrefix(trimmed, "if ") || strings.HasPrefix(trimmed, "switch ")):
				entry := tail
				if mentions {
					entry = nil
				}
				sw := strings.HasPrefix(trimmed, "switch ")
				stack = append(stack, cellBlock{entry: entry, cases: sw, bare: !sw})
				begin(nil)
				if !sw {
					begin(entry) // the then arm
				}
			case opens && strings.HasPrefix(trimmed, "for "):
				stack = append(stack, cellBlock{})
				tail = nil
			case trimmed == "} else {" && len(stack) > 0 && !stack[len(stack)-1].cases:
				stack[len(stack)-1].bare = false
				begin(stack[len(stack)-1].entry)
			case (strings.HasPrefix(trimmed, "case ") || trimmed == "default:") && strings.HasSuffix(trimmed, ":") && len(stack) > 0 && stack[len(stack)-1].cases:
				begin(stack[len(stack)-1].entry)
			case trimmed == "}" && len(stack) > 0:
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if !top.bare || !sameCall(tail, top.entry) {
					tail = nil
				}
			case !mentions && oneLineBlock(trimmed):
				// A whole block on one line that never names the
				// writer (an optional field's count) leaves the state.
			case strings.ContainsAny(trimmed, "{}") || strings.HasPrefix(trimmed, "case ") || trimmed == "default:" || strings.HasPrefix(trimmed, "else"):
				stopped, tail = true, nil
			case mentions:
				tail = nil
			}
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
		if len(run) == 0 && tail != nil && !stopped {
			// The run starts in tail's state.
			run, texts, lead = []rwCall{*tail}, [][]string{make([]string, len(tail.args))}, 1
		}
		run = append(run, c)
		texts = append(texts, ts)
	}
	flush()
	return strings.Join(out, "\n")
}

// oneLineBlock reports an `if` whose block opens and closes on its line,
// `if _, absent := v.F.(Absent); absent { fields-- }`.
func oneLineBlock(line string) bool {
	return strings.HasPrefix(line, "if ") && strings.HasSuffix(line, "}") && strings.Count(line, "{") == strings.Count(line, "}")
}

// cellFunc is a cell writer's codec function: key names its one variant,
// which starts in the state start (nil until a call asks for it); emit
// writes the variant once the function's text is known (wireFunc), and
// pending reports a variant asked for before.
type cellFunc struct {
	key     pkgKey
	start   *rwCall
	emit    func(name string, start rwCall)
	pending bool
}

// cellEntryKey is the package-scope key of the entry variant of the codec
// function of key (reserveWires).
func cellEntryKey(key pkgKey) pkgKey {
	return pkgKey{"cellentry", key.kind + " " + key.obj.(string)}
}

// cellEntryCall is line with its call of a cell codec function on *w,
// `*w = f(env, *w, v)`, made a call of f's variant for the state start.
// So the callee's first run knows it starts in start's state, as an arm
// of the caller would: a record or list in a key's place drops its comma
// test. Any other line is returned as it is.
func (l *lowerer) cellEntryCall(line, w string, start rwCall) string {
	indent := line[:len(line)-len(strings.TrimLeft(line, "\t "))]
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "*"+w+" = ")
	if !ok {
		return line
	}
	name, args, ok := strings.Cut(rest, "(")
	if !ok || l.cellFuncs[name] == nil {
		return line
	}
	v, ok := l.cellVariant(name, start)
	if !ok {
		return line
	}
	return indent + "*" + w + " = " + v + "(" + args
}

// cellVariant is the Go name of the variant of the cell codec function
// name that starts in start's state, written when name's text is known.
// A function has one variant, for the first state a call asks for; ok is
// false for another state, or when the naming pass reserved none.
func (l *lowerer) cellVariant(name string, start rwCall) (string, bool) {
	cf := l.cellFuncs[name]
	v, reserved := l.pkgNames[cf.key]
	switch {
	case !reserved:
		return "", false
	case cf.start != nil:
		return v, sameCall(cf.start, &start)
	}
	cf.start = &start
	if cf.emit != nil {
		cf.emit(v, start)
	} else {
		cf.pending = true
	}
	return v, true
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
// the provider's laws rewrite it, the run's own calls otherwise, without
// its first lead calls (carried, cellEvents). last is the run's last call
// when its arguments are constants, else nil.
func (l *lowerer) cellRun(cw cellWriter, env, w string, run []rwCall, texts [][]string, lead int, site syntax.Node) (out []string, last *rwCall) {
	calls, rewritten := run[lead:], false
	if rw := l.cellRewriter(cw.prov); rw != nil {
		if got, changed := rw.run(cw.prov, run); changed {
			calls, rewritten = got, true
		}
	}
	if !rewritten {
		texts = texts[lead:]
	}
	if n := len(calls); n > 0 {
		c := calls[n-1]
		last = &c
		for _, a := range c.args {
			if a.c == nil {
				last = nil
			}
		}
	}
	emit := func(c rwCall, args []string) string {
		self := w
		if !c.fn.Ptr {
			self = "(*" + w + ")"
		}
		return l.wireCallFunc(c.fn, env, site, append([]string{self}, args...)...)
	}
	used := map[[2]int]bool{}
	for k, c := range calls {
		var args []string
		varies := false
		for j, a := range c.args {
			switch {
			case a.c != nil:
				args = append(args, l.constGo(c.fn.Sig.Params[j+1].Type, a.c, site))
			case !rewritten:
				args, varies = append(args, texts[k][j]), true
			default:
				pos, arg := rwPlace(a.text)
				used[[2]int{pos, arg}] = true
				args, varies = append(args, texts[pos][arg]), true
			}
		}
		if varies && rewriteLowering != "reference" {
			if lines, ok := l.cellBranch(c, args, emit, env, site); ok {
				out = append(out, lines...)
				continue
			}
		}
		out = append(out, emit(c, args))
	}
	if rewritten {
		// An argument the rewritten run no longer passes (an unfold that
		// drops it, as Bool(b) { raw("b") } does) is still read, so a
		// variable that names only it stays used in Go.
		for k := lead; k < len(run); k++ {
			for j, a := range run[k].args {
				if a.c == nil && !used[[2]int{k, j}] {
					out = append(out, "_ = "+texts[k][j])
				}
			}
		}
	}
	return out, last
}

// cellBranch is the Go lines of c, a call of a provider function whose
// body is an if of a pure condition (branchDef), as that if, written in
// the codec function: by definition the same, and each arm's calls are
// on the cell's value where the codec function holds it, so Go inlines
// them there (a plain key's quotes are one append of the buffer, with no
// write through a pointer). args are c's arguments' Go text.
func (l *lowerer) cellBranch(c rwCall, args []string, emit func(rwCall, []string) string, env string, site syntax.Node) ([]string, bool) {
	tagged := rwCall{fn: c.fn}
	for j, a := range c.args {
		if a.c == nil {
			a = rwArg{text: "#" + strconv.Itoa(j)}
		}
		tagged.args = append(tagged.args, a)
	}
	cond, condArgs, then, els, ok := branchDef(tagged)
	if !ok {
		return nil, false
	}
	texts := func(fn *check.Func, as []rwArg, skip int) []string {
		var out []string
		for i, a := range as {
			if a.c != nil {
				out = append(out, l.constGo(fn.Sig.Params[i+skip].Type, a.c, site))
				continue
			}
			j, _ := strconv.Atoi(strings.TrimPrefix(a.text, "#"))
			out = append(out, args[j])
		}
		return out
	}
	arm := func(calls []rwCall) []string {
		var out []string
		for _, ac := range calls {
			out = append(out, emit(ac, texts(ac.fn, ac.args, 1)))
		}
		return out
	}
	lines := []string{"if " + l.wireCallFunc(cond, env, site, texts(cond, condArgs, 0)...) + " {"}
	lines = append(lines, arm(then)...)
	lines = append(lines, "} else {")
	lines = append(lines, arm(els)...)
	return append(lines, "}"), true
}

// idempotent is c when the provider's laws rewrite c; c to c alone (so a
// state c ended in is c's own: c changes it no more), else nil.
func (l *lowerer) idempotent(prov *check.Named, c *rwCall) *rwCall {
	if c == nil {
		return nil
	}
	rw := l.cellRewriter(prov)
	if rw == nil {
		return nil
	}
	got, changed := rw.run(prov, []rwCall{*c, *c})
	if !changed || len(got) != 1 || !sameCall(&got[0], c) {
		return nil
	}
	return c
}

// sameCall reports two calls of one function on equal constants.
func sameCall(a, b *rwCall) bool {
	if a == nil || b == nil || a.fn != b.fn || len(a.args) != len(b.args) {
		return false
	}
	for i := range a.args {
		x, y := a.args[i].c, b.args[i].c
		if x == nil || y == nil || x.Kind() != y.Kind() || !constant.Compare(x, token.EQL, y) {
			return false
		}
	}
	return true
}

// cellRewriter is the rewriter of the cell provider prov's laws: the run
// build's own for a provider of the root package, else one over the laws
// prov's package runs, on the evidence of the ledger of the module that
// declares it (evidenceFor); nil when the build takes no evidence for it.
// Where the provider lives decides nothing else (owner, 2026-11-12: the
// standard library gets no special treatment).
func (l *lowerer) cellRewriter(prov *check.Named) *rewriter {
	if rewriteLowering == "reference" || prov.Obj.Pkg == nil {
		return nil
	}
	if prov.Obj.Pkg == l.pkg {
		return l.rewrite
	}
	if l.cellRewrites == nil {
		l.cellRewrites = map[*check.Package]*rewriter{}
	}
	rw, ok := l.cellRewrites[prov.Obj.Pkg]
	if !ok {
		if ev := l.evidenceFor(prov.Obj.Pkg); ev != nil {
			rw = newRewriter(prov.Obj.Pkg, ev)
		}
		l.cellRewrites[prov.Obj.Pkg] = rw
	}
	return rw
}

// shippedEvidence is the evidence of the ledger shipped with the packages
// the compiler ships (shippedLedger) for the laws one of them runs.
type shippedEvidence struct {
	entries map[string]LedgerEntry
	rules   map[string]rwRule
	digests map[string]string
}

// newShippedEvidence reads the shipped ledger for the laws of pkg.
func newShippedEvidence(pkg *check.Package) *shippedEvidence {
	entries, err := ParseLedger(shippedLedger())
	if err != nil {
		ice(nil, "the shipped law ledger: %v", err)
	}
	e := &shippedEvidence{entries: entries, rules: map[string]rwRule{}, digests: map[string]string{}}
	for prov, rules := range newRewriter(pkg, nil).rules {
		for _, r := range rules {
			e.rules[ledgerKey(LedgerNames(nil).law(r.law), LedgerNames(nil).provider(prov), "")] = r
		}
	}
	return e
}

// Admit admits a law whose shipped ledger entry is proved and current. A
// proved law holds at every instance, so in's constants are not looked up.
func (e *shippedEvidence) Admit(law *check.Law, prov *check.Named, in RewriteInstance) (bool, string) {
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
