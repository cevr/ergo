package lower

import (
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Bodies (spec D4). Statements lower to Go statements in the source
// function's own Go function: a with block, a catch and a match are Go
// blocks and switches inside it, never closures, so return, try and defer
// keep their frame. Every operand is evaluated once, left to right
// (expr.go); a statement that yields no Go value is written for its
// effects alone.

// frame is the Go function being written: a declared function or a
// function literal.
type frame struct {
	result   check.Type // nil when it returns no value
	fallible bool
	// unit marks a function value with no result: its Go form returns
	// struct{}{} before the error, the uniform shape (typeCtx.funcType;
	// errReturn writes each such return).
	unit bool
	// scope is the Go name of the frame scope, "" when the frame opens
	// none (stage D3b, Rule 2); dest is the destination parameter of a
	// frame that returns a Task, "" otherwise.
	scope, dest string
	// with is the Go name of the scope of the innermost owning with around
	// the code being written, "" outside one (calls.go, with): the scope
	// that owns the tasks the code starts or receives (frameScope).
	with string
	// polls reports a frame whose footprint is not empty (Info.Footprints;
	// runtime.md, I1): it checks for a stop before each capability call's
	// operands and at the end of each loop iteration. Entry placement is a separate checker
	// fact read by entryPoll. A pure frame has no point.
	polls bool
}

// newFrame is the only frame constructor. Every frame-opening path uses it,
// so the source-derived frame catalog sees new paths at their call sites.
func newFrame(result check.Type, fallible, polls bool) *frame {
	return &frame{result: result, fallible: fallible, polls: polls}
}

// framePolls reads the footprint the checker recorded for a frame's key
// (Info.Footprints) from info, the package that declares it. The lowering
// never derives a footprint: a frame with no entry is a checker bug.
func framePolls(info *check.Info, key syntax.Node) bool {
	return !frameFootprint(info, key).Empty()
}

func frameFootprint(info *check.Info, key syntax.Node) check.Footprint {
	fp, ok := info.Footprints[key]
	if !ok {
		ice(key, "a frame with no Footprints entry")
	}
	return fp
}

// entryPoll reads the checker's placement fact. Call after the environment
// and any owned frame scope are bound, before executing the source body.
func (fc *funcCtx) entryPoll(info *check.Info, key syntax.Node) {
	if frameFootprint(info, key).EntryPoll {
		fc.at(key)
		fc.poll(key)
	}
}

// poll writes the interruption point of an impure frame (runtime.md, I1):
// the running task's Check, which unwinds the task once it is asked to stop
// outside a mask. A pure frame writes nothing.
func (fc *funcCtx) poll(site syntax.Node) {
	if fc.frame == nil {
		ice(site, "an interruption point outside a frame")
	}
	if fc.frame.polls {
		fc.line(taskOf(fc.envExpr(site)) + ".Check()")
	}
}

// loopCtx is an enclosing for loop: break and continue inside a match,
// which lowers to a Go switch, name it by a label (spec D4: match does not
// own break and continue).
type loopCtx struct {
	label    string
	switches int // Go switches between the loop and the current statement
	// next labels the back-edge poll that ends each iteration of a polling
	// loop: a continue jumps to it (loopBody). Empty until a continue needs it.
	next string
}

// funcCtx writes one Go function body.
type funcCtx struct {
	typeCtx
	scope      *namer
	out        *strings.Builder
	depth      int
	locals     map[*check.Var]string
	frame      *frame
	acquiring  *acquireFlow // a native acquisition callback, in this same source frame
	env        string       // the current environment pointer, which every Go function the lowering writes binds
	directives bool
	loops      []*loopCtx
	// tries counts the try expressions being lowered: inside one, every
	// fallible value propagates (196r: try covers the expression to its
	// right).
	tries int
	// owner is the Ergo name of the source declaration being lowered (a
	// function, a package let, a context), the Func of its call identities
	// (#230); a function literal inherits it.
	owner string
	// lastPos is the position of the last //line directive written.
	lastPos syntax.Pos
	// lastNode is the node whose position lastPos is.
	lastNode syntax.Node
	// witness names the Go parameter that holds the operations of each of
	// the function's type parameters a method set constrains (witness.go).
	witness map[*check.TypeParam]string
	// header counts the statement headers being lowered (header.go): inside
	// one, a composite literal of the TypeName form is parenthesized.
	header int
}

func newFuncCtx(l *lowerer, scope *namer, directives bool) *funcCtx {
	return &funcCtx{typeCtx: typeCtx{l: l, params: map[*check.TypeParam]string{}}, out: &strings.Builder{}, scope: scope, locals: map[*check.Var]string{}, directives: directives, witness: map[*check.TypeParam]string{}}
}

// innerFuncCtx opens a new frame while retaining the enclosing signature's
// type bindings and witnesses. These facts and captured local names are
// shared; execution state (environment, frame, loops and tries) is fresh.
func (fc *funcCtx) innerFuncCtx() *funcCtx {
	return &funcCtx{typeCtx: fc.typeCtx, out: &strings.Builder{}, scope: fc.scope, locals: fc.locals, directives: fc.directives, owner: fc.owner, witness: fc.witness}
}

// line writes one Go line at the current depth.
func (fc *funcCtx) line(s string) {
	fc.out.WriteString(strings.Repeat("\t", fc.depth) + s + "\n")
}

// at writes the //line directive of n, so the Go lines that follow map to
// n's source line and column.
func (fc *funcCtx) at(n syntax.Node) {
	fc.site = n
	if fc.directives && n != nil && n.Pos().IsValid() {
		fc.out.WriteString(fc.l.directive(n))
		fc.lastPos, fc.lastNode = n.Pos(), n
	}
}

// atSite prefixes val, the Go text of an operation that can raise a
// defect (an index or slice out of range, a division by zero, a negative
// shift count, a nil dereference), with the inline /*line*/ directive of
// pos, the operation's .ergo position. The defect then names that line
// exactly, on whatever Go line go/format puts the operation (stage C
// round 2 review m3).
func (fc *funcCtx) atSite(n syntax.Node, pos syntax.Pos, val string) string {
	if !fc.directives || !pos.IsValid() {
		return val
	}
	_, src, line, column := fc.l.at(n, pos)
	return InlineDirective(src, line, column) + val
}

func (fc *funcCtx) temp(base string) string { return fc.scope.alloc(base) }

// body writes a function body and closes it so Go sees every path end: a
// value-returning body that may fall off its end ends in a panic, which a
// checked program never reaches, and a fallible body with no value returns
// nil.
func (fc *funcCtx) body(list []syntax.Stmt) {
	if fc.yieldList(list, dest{kind: destReturn}) {
		return
	}
	switch {
	case fc.frame.result != nil:
		fc.line("panic(\"ergo: unreachable: the function's end\")")
	case fc.frame.fallible:
		fc.line(fc.errReturn("nil"))
	}
}

// errReturn is the Go return of the error err, with no value, from the
// current frame: a unit frame (a function value with no result) returns
// struct{}{} first.
func (fc *funcCtx) errReturn(err string) string {
	if fc.frame.unit {
		return "return struct{}{}, " + err
	}
	return "return " + err
}

// stmts writes a statement list and reports whether its Go form ends in a
// terminating statement (Go spec, Terminating statements).
func (fc *funcCtx) stmts(list []syntax.Stmt) bool {
	terminates := false
	for _, s := range list {
		terminates = fc.stmt(s)
	}
	return terminates
}

// block writes a nested statement list one level deeper.
func (fc *funcCtx) block(list []syntax.Stmt) bool {
	fc.depth++
	t := fc.stmts(list)
	fc.depth--
	return t
}

// stmt writes one statement and reports whether it is terminating in Go.
func (fc *funcCtx) stmt(s syntax.Stmt) bool {
	fc.at(s)
	switch s := s.(type) {
	case *syntax.DeclStmt:
		fc.let(s.Decl)
	case *syntax.AssignStmt:
		fc.assign(s)
	case *syntax.ReturnStmt:
		fc.ret(s)
		return true
	case *syntax.ExprStmt:
		fc.exprStmt(s.X)
	case *syntax.BlockStmt:
		fc.line("{")
		t := fc.block(s.List)
		fc.line("}")
		return t
	case *syntax.IfStmt:
		return fc.ifYield(s, dest{})
	case *syntax.ForStmt:
		return fc.forStmt(s)
	case *syntax.BranchStmt:
		fc.branch(s)
		return true
	case *syntax.TypeSwitchStmt:
		return fc.typeMatch(s, dest{})
	case *syntax.SwitchStmt:
		return fc.valueMatch(s, dest{})
	case *syntax.RangeStmt:
		return fc.rangeStmt(s)
	case *syntax.AssertStmt:
		fc.assert(s) // the test build only (tests.go)
	default:
		unreachable(s, ruleParser, "a statement of the kind %T", s)
	}
	return false
}

// let lowers a local let: a Go variable of the checked type, so a literal
// keeps its one type (196u).
func (fc *funcCtx) let(d *syntax.VarDecl) {
	for _, spec := range d.Specs {
		if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) > 1 {
			fc.letPattern(spec)
			continue
		}
		id := spec.Names[0]
		if p := fc.l.info.Patterns[id]; p != nil && p.Kind != check.PatBind {
			// A known single name remains in Names in the frozen syntax,
			// but has no binder. The checker proved this test irrefutable.
			fc.discard(spec.Values[0])
			continue
		}
		if w, ok := fc.l.info.Defs[id].(*check.Context); ok && w.Local {
			// A local context is a recipe (rows.md, Context 9): its let
			// lowers to nothing, and each with that names it builds its
			// providers in place (calls.go, with).
			continue
		}
		v, ok := fc.l.info.Defs[id].(*check.Var)
		if id.Name == "_" {
			if len(spec.Values) == 1 {
				fc.discard(spec.Values[0])
			}
			continue
		}
		if !ok {
			ice(id, "the let %s has no Defs entry", id.Name)
		}
		goType := fc.goType(v.Type)
		if len(spec.Values) == 0 {
			name := fc.declare(v)
			if def := fc.defaultValue(v.Type); def != "" {
				fc.line("var " + name + " " + goType + " = " + def)
			} else {
				fc.line("var " + name + " " + goType)
			}
		} else {
			val := fc.assigned(spec.Values[0])
			name := fc.declare(v)
			fc.line("var " + name + " " + goType + " = " + val)
		}
		if !fc.l.reads[v] {
			fc.line("_ = " + fc.locals[v])
		}
	}
}

// declare names a local variable.
func (fc *funcCtx) declare(v *check.Var) string {
	name := fc.scope.alloc(v.Name)
	fc.locals[v] = name
	return name
}

// assign lowers x = e and x op= e. The target's operands are evaluated
// first, then the value (Go's two-phase assignment); an op-assignment reads
// its target before the value.
func (fc *funcCtx) assign(s *syntax.AssignStmt) {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		unreachable(s, ruleParser, "an assignment of several values")
	}
	op := ""
	switch s.Tok {
	case syntax.ASSIGN:
	default:
		text := s.Tok.String()
		if !strings.HasSuffix(text, "=") || text == "==" || text == ":=" {
			unreachable(s, ruleParser, "the assignment %s", text)
		}
		op = strings.TrimSuffix(text, "=")
	}
	rhsEmits := fc.emits(s.Rhs[0])
	target := fc.target(s.Lhs[0], rhsEmits)
	if op == "" {
		val := fc.assigned(s.Rhs[0])
		fc.line(target + " = " + val)
		return
	}
	if rhsEmits {
		old := fc.temp("old")
		fc.line(old + " := " + target)
		val := fc.value(s.Rhs[0])
		fc.line(target + " = " + old + " " + op + " " + val)
		return
	}
	fc.line(target + " " + op + "= " + fc.value(s.Rhs[0]))
}

// target is an assignment target's Go text: a place. One order holds for
// every place (spec D4): the place is fixed, left to right, before anything
// evaluated after it (later). A place is a variable, a dereferenced pointer
// *p, a field of a place, a field through a pointer, an element of a slice
// or an element of an array place, nested in any order. The values that
// pick the place, a pointer, a slice and an index, are bound to temporaries
// when something after them writes statements, so a later operand that
// reassigns a variable cannot move the place.
func (fc *funcCtx) target(x syntax.Expr, later bool) string {
	switch x := x.(type) {
	case *syntax.Ident:
		if x.Name == "_" {
			return "_" // a discard: _ = e evaluates e
		}
		return fc.ident(x)
	case *syntax.ParenExpr:
		return fc.target(x.X, later)
	case *syntax.StarExpr:
		// *p: the place p points at, p bound first.
		base := fc.value(x.X)
		if later {
			base = fc.spill(base)
		}
		return "(*" + base + ")"
	case *syntax.SelectorExpr:
		if _, isPtr := fc.typeOf(x.X).(*check.Pointer); isPtr {
			base := fc.value(x.X)
			if later {
				base = fc.spill(base)
			}
			return base + "." + fc.fieldName(x)
		}
		return fc.target(x.X, later) + "." + fc.fieldName(x)
	case *syntax.IndexExpr:
		if len(x.Indices) != 1 {
			unreachable(x, ruleAccepted, "an assignment target indexed by %d expressions", len(x.Indices))
		}
		var base string
		indexEmits := fc.emits(x.Indices[0])
		switch fc.typeOf(x.X).(type) {
		case *check.Slice, *check.Map:
			// A Map write is total (D-D4, the Map rule): it inserts or
			// replaces the entry.
			base = fc.value(x.X)
			if later || indexEmits {
				base = fc.spill(base)
			}
		default:
			base = fc.target(x.X, later || indexEmits)
		}
		index := fc.value(x.Indices[0])
		if later && !fc.isConst(x.Indices[0]) {
			index = fc.spill(index)
		}
		return base + "[" + index + "]"
	}
	unreachable(x, ruleAccepted, "an assignment target of the kind %T", x)
	return ""
}

// isPlace reports whether x is a place target can lower: an addressable
// Go expression.
func (fc *funcCtx) isPlace(x syntax.Expr) bool {
	switch x := x.(type) {
	case *syntax.Ident:
		_, ok := fc.l.info.Uses[x].(*check.Var)
		return ok
	case *syntax.ParenExpr:
		return fc.isPlace(x.X)
	case *syntax.StarExpr:
		return true
	case *syntax.SelectorExpr:
		if id, ok := x.X.(*syntax.Ident); ok {
			if _, isPkg := fc.l.info.Uses[id].(*check.PkgName); isPkg {
				return false
			}
		}
		if _, isPtr := fc.typeOf(x.X).(*check.Pointer); isPtr {
			return true
		}
		return fc.isPlace(x.X)
	case *syntax.IndexExpr:
		if len(x.Indices) != 1 {
			return false
		}
		switch fc.typeOf(x.X).(type) {
		case *check.Slice:
			return true
		case *check.Map, *check.Signature:
			return false
		}
		return fc.isPlace(x.X)
	}
	return false
}

// place is the address of the receiver x of a *T function: the place's
// address when x is a place, fixed before anything later (target); else
// x's value in a temporary, whose address Go can take.
func (fc *funcCtx) place(x syntax.Expr, later bool) string {
	if fc.isPlace(x) {
		return "&" + fc.target(x, later)
	}
	t := fc.temp("r")
	fc.line(t + " := " + fc.value(x))
	return "&" + t
}

// ret lowers return. A value of an error type is a failure (types.md, Error
// types 2); an untried fallible value returns its value and error as they
// are.
func (fc *funcCtx) ret(s *syntax.ReturnStmt) {
	f := fc.frame
	if len(s.Results) > 1 {
		unreachable(s, ruleOneResult, "a return of %d values", len(s.Results))
	}
	if len(s.Results) == 0 {
		if f.fallible {
			fc.sourceReturn("nil")
		} else {
			fc.sourceReturn()
		}
		return
	}
	fc.retExpr(s.Results[0])
}

// retExpr is shared by explicit return and a checked function-body tail.
// The source expression is retained so calls and directives keep their site.
func (fc *funcCtx) retExpr(x syntax.Expr) {
	f := fc.frame
	// Whether the value is a failure is decided by the returning body's
	// own view (owners.go): a type only a test's row names is a value in
	// a production body, in the test build too.
	if !fc.fallibleExpr(x) && fc.l.isErrorValue(fc.typeOf(x), fc.l.pkg.OwnerOf(x)) {
		fc.fail(fc.value(x))
		return
	}
	if fc.fallibleExpr(x) {
		o := fc.expr(x)
		switch {
		case f.result != nil && o.val != "":
			fc.returnFallible(x, o)
		case f.result != nil:
			ice(x, "a fallible return with no value in a function with a result")
		default:
			if o.val != "" {
				fc.line("_ = " + o.val)
			}
			fc.sourceReturn(o.err)
		}
		return
	}
	if f.result == nil {
		// A no-value call returned for its effect: return f().
		fc.exprStmt(x)
		if f.fallible {
			fc.sourceReturn("nil")
		} else {
			fc.sourceReturn()
		}
		return
	}
	val := fc.moved(fc.assigned(x))
	if f.fallible {
		fc.sourceReturn(val, "nil")
	} else {
		fc.sourceReturn(val)
	}
}

// fail leaves the current frame with err (rows.md, Escapes): the frame's
// zero value and the error.
func (fc *funcCtx) fail(err string) {
	if fc.acquiring != nil {
		fc.line("return nil, " + err)
		return
	}
	f := fc.frame
	if !f.fallible {
		fc.line("panic(" + err + ")")
		return
	}
	if f.result != nil {
		fc.line("return " + fc.zero(f.result) + ", " + err)
		return
	}
	fc.line(fc.errReturn(err))
}

// exprStmt writes an expression statement for its effects.
func (fc *funcCtx) exprStmt(x syntax.Expr) {
	fc.discard(x)
}

// discard evaluates x and drops its value: a call is a Go call statement,
// and any other value is assigned to the blank identifier.
func (fc *funcCtx) discard(x syntax.Expr) {
	x = unparen(x)
	// A package function's explicit type arguments construct a pure function
	// value. Discarding it evaluates no receiver or argument and needs no
	// adapter (in particular, no inferred generic-value instance).
	if ix, ok := x.(*syntax.IndexExpr); ok {
		if id, named := unparen(ix.X).(*syntax.Ident); named {
			if _, function := fc.l.info.Uses[id].(*check.Func); function {
				return
			}
		}
	}
	if call, ok := x.(*syntax.CallExpr); ok {
		if c := fc.l.info.Calls[call]; c != nil && c.Kind != check.CallWith && c.Kind != check.CallConversion && c.Kind != check.CallLen && !(fc.tries > 0 && fc.fallibleExpr(call)) {
			if text := fc.callStmt(call); text != "" {
				fc.line(text)
			}
			return
		}
	}
	o := fc.expr(x)
	if o.err != "" {
		if fc.tries == 0 {
			ice(x, "an untried fallible expression in a statement")
		}
		fc.propagate(o.err)
	}
	if o.val != "" {
		fc.line("_ = " + o.val)
	}
}

// forStmt lowers a conditional loop. The condition is evaluated at the top
// of every iteration, inside the loop, so a condition with effects runs once
// per test; a loop with no condition and no break is terminating.
func (fc *funcCtx) forStmt(s *syntax.ForStmt) bool {
	loop := &loopCtx{}
	fc.loops = append(fc.loops, loop)
	saved := fc.out
	fc.out = &strings.Builder{}
	breaks := false
	// The poll ends the iteration (loopBody); a condition that writes
	// statements runs at the top of the next one.
	if s.Cond == nil || !fc.emits(s.Cond) {
		if s.Cond == nil {
			fc.line("for {")
		} else {
			fc.line("for " + fc.headerValue(s.Cond) + " {")
			breaks = true
		}
	} else {
		fc.line("for {")
		fc.depth++
		cond := fc.headerValue(s.Cond)
		fc.line("if !(" + cond + ") {")
		fc.line("\tbreak")
		fc.line("}")
		fc.depth--
		breaks = true
	}
	breaks = fc.containsBreak(s.Body) || breaks
	fc.loopBody(s, loop, s.Body.List)
	fc.line("}")
	body := fc.out.String()
	fc.out = saved
	fc.loops = fc.loops[:len(fc.loops)-1]
	if loop.label != "" {
		fc.out.WriteString(loop.label + ":\n")
	}
	fc.out.WriteString(body)
	return !breaks
}

// rangeStmt lowers a for-in over the domain the checker recorded
// (Info.Loops, D-D4). A slice and a Map are Go's range; a Range[T] counts
// from Start up to End, End excluded, evaluated once; a Seq[T] is Go's
// range over func: the Seq keeps the uniform function shape of every Ergo
// function value (funcType), so the loop adapts it to iter.Seq[T]'s
// shape, and a break makes yield return false. Each binding is a Go
// variable declared in the body, so a function literal captures one
// iteration's value. A for-in can complete, so it is never terminating.
func (fc *funcCtx) rangeStmt(s *syntax.RangeStmt) bool {
	l := fc.l.info.Loops[s]
	if l == nil {
		ice(s, "a for-in with no Loops entry")
	}
	x := fc.headerValue(s.X)
	if l.Domain == check.LoopRange || l.Domain == check.LoopSeq {
		x = fc.spill(x)
	}
	loop := &loopCtx{}
	fc.loops = append(fc.loops, loop)
	saved := fc.out
	fc.out = &strings.Builder{}
	pos, elem := "_", "_"
	if bound(s.Key) {
		pos = fc.temp("k")
	}
	if bound(s.Value) {
		elem = fc.temp("v")
	}
	switch l.Domain {
	case check.LoopSlice, check.LoopMap:
		switch {
		case pos == "_" && elem == "_":
			fc.line("for range " + x + " {")
		case elem == "_":
			fc.line("for " + pos + " := range " + x + " {")
		default:
			fc.line("for " + pos + ", " + elem + " := range " + x + " {")
		}
	case check.LoopRange:
		n, ok := fc.typeOf(s.X).(*check.Named)
		if !ok {
			ice(s.X, "a Range domain typed %s", fc.typeOf(s.X))
		}
		td := fc.l.typeDecl(n.Obj, s.X)
		i := fc.temp("i")
		fc.line("for " + i + " := " + x + "." + td.fieldName["Start"] + "; " + i + " < " + x + "." + td.fieldName["End"] + "; " + i + "++ {")
		elem = i
	case check.LoopSeq:
		v := elem
		if v == "_" {
			v = fc.temp("v")
		}
		elemType := fc.goType(l.Elem)
		yield := fc.temp("yield")
		env := fc.envExpr(s)
		// The Seq's row is empty (its type has none), so its error is nil.
		callback := fc.envClosure(callerEnv, "_", s, func(_ string) ([]string, string, string) {
			return []string{v + " " + elemType}, " (bool, error)", "return " + yield + "(" + v + "), nil\n"
		})
		adapter := "func(" + yield + " func(" + elemType + ") bool) { _, _ = " + x + "(" + env + ", " + callback + ") }"
		if elem == "_" {
			fc.line("for range " + adapter + " {")
		} else {
			fc.line("for " + elem + " := range " + adapter + " {")
		}
	default:
		ice(s, "a for-in over the unknown domain %d", l.Domain)
	}
	fc.depth++
	// The bindings receive the element first; the poll ends the iteration
	// (loopBody).
	fc.loopBind(s.Key, pos, l.Pos)
	fc.loopBind(s.Value, elem, l.Elem)
	fc.depth--
	fc.loopBody(s, loop, s.Body.List)
	fc.line("}")
	body := fc.out.String()
	fc.out = saved
	fc.loops = fc.loops[:len(fc.loops)-1]
	if loop.label != "" {
		fc.out.WriteString(loop.label + ":\n")
	}
	fc.out.WriteString(body)
	return false
}

// loopBody writes a loop's body and then its back-edge poll (runtime.md,
// I1): the poll ends each iteration, on every path that continues the loop,
// since a continue jumps to it (branch). It never heads the body, so it
// never separates a delivered element (a Seq's or a Values loop's yield, a
// binding, a condition) from the body that receives it. When a continue
// jumps to the poll, the body is a Go block of its own, so the goto skips
// none of its declarations (Go spec, Goto statements).
func (fc *funcCtx) loopBody(s syntax.Stmt, loop *loopCtx, list []syntax.Stmt) {
	saved := fc.out
	fc.out = &strings.Builder{}
	fc.block(list)
	text := fc.out.String()
	fc.out = saved
	if loop.next == "" {
		fc.out.WriteString(text)
	} else {
		fc.depth++
		fc.line("{")
		fc.out.WriteString(text)
		fc.line("}")
		fc.depth--
		fc.line(loop.next + ":")
	}
	if fc.frame.polls {
		fc.depth++
		fc.at(s)
		fc.poll(s)
		fc.depth--
	}
}

// bound reports a for binding that binds: present and not `_`.
func bound(p syntax.Expr) bool {
	if p == nil {
		return false
	}
	id, ok := p.(*syntax.Ident)
	return !ok || id.Name != "_"
}

// loopBind binds one for binding to val, the Go variable of the position
// or the element, of type t: a name, or an irrefutable pattern the checker
// proved (Info.Patterns).
func (fc *funcCtx) loopBind(p syntax.Expr, val string, t check.Type) {
	if !bound(p) {
		return
	}
	var binds []patBind
	id, _ := p.(*syntax.Ident)
	if v, ok := fc.l.info.Defs[id].(*check.Var); ok && id != nil {
		binds = append(binds, patBind{v, val})
	} else if pat := fc.l.info.Patterns[p]; pat != nil {
		// A pattern may bind nothing (`for T in ts` tests the type).
		fc.line("_ = " + val)
		var tests []string
		fc.pattern(pat, val, t, p, &tests, &binds)
	} else {
		ice(p, "a for binding with neither a Defs nor a Patterns entry")
	}
	for _, b := range binds {
		name := fc.declare(b.v)
		fc.line("var " + name + " " + fc.goType(b.v.Type) + " = " + b.val)
		if !fc.l.reads[b.v] {
			fc.line("_ = " + name)
		}
	}
}

// containsBreak reports a break of this loop in its body: one not inside a
// nested loop.
func (fc *funcCtx) containsBreak(body *syntax.BlockStmt) bool {
	found := false
	var walk func(n syntax.Node) bool
	walk = func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ForStmt, *syntax.RangeStmt, *syntax.FuncLit:
			return false
		case *syntax.BranchStmt:
			if n.Tok == syntax.BREAK {
				found = true
			}
		}
		return !found
	}
	for _, s := range body.List {
		syntax.Inspect(s, walk)
	}
	return found
}

// branch lowers break and continue to the innermost loop, by a label when a
// Go switch lies between them.
func (fc *funcCtx) branch(s *syntax.BranchStmt) {
	if len(fc.loops) == 0 {
		ice(s, "a break or continue outside a loop")
	}
	word := "break"
	if s.Tok == syntax.CONTINUE {
		word = "continue"
	}
	if a := fc.acquiring; a != nil && len(fc.loops) <= a.loops {
		code := "2"
		if s.Tok == syntax.CONTINUE {
			code = "3"
		}
		fc.line(a.flow + " = " + code)
		fc.line("return nil, nil")
		return
	}
	loop := fc.loops[len(fc.loops)-1]
	if s.Tok == syntax.CONTINUE && fc.frame.polls {
		// The back-edge poll ends the iteration (loopBody), so a continue
		// jumps to it rather than past it.
		if loop.next == "" {
			loop.next = fc.temp("next")
		}
		fc.line("goto " + loop.next)
		return
	}
	if loop.switches == 0 {
		fc.line(word)
		return
	}
	if loop.label == "" {
		loop.label = fc.temp("loop")
	}
	fc.line(word + " " + loop.label)
}

// enterSwitch and leaveSwitch track the Go switches inside the innermost
// loop.
func (fc *funcCtx) enterSwitch() {
	if len(fc.loops) > 0 {
		fc.loops[len(fc.loops)-1].switches++
	}
}

func (fc *funcCtx) leaveSwitch() {
	if len(fc.loops) > 0 {
		fc.loops[len(fc.loops)-1].switches--
	}
}

// typeMatch lowers a match on one subject with type patterns to a Go type
// switch. The checker proved it total (Info contract 3), so it has no
// default arm unless it writes case _.
func (fc *funcCtx) typeMatch(s *syntax.TypeSwitchStmt, d dest) bool {
	m := fc.l.info.Matches[s]
	if m == nil {
		ice(s, "a match with no Matches entry")
	}
	if !fc.simpleTypeMatch(s) {
		subjects := make([]syntax.Expr, len(s.Subjects))
		for i, g := range s.Subjects {
			subjects[i] = g.X
		}
		return fc.patternMatch(s, subjects, s.Clauses, d)
	}
	subject := fc.headerValue(s.Subjects[0].X)
	type arm struct {
		types []string
		as    *check.Var
		body  []syntax.Stmt
		def   bool
	}
	var arms []arm
	bind := false
	seen := map[string]bool{}
	caseType := func(t check.Type, item syntax.Node) string {
		goType := fc.goType(t)
		if seen[goType] {
			unreachable(item, ruleCoveredCase, "a case of the Go type %s, which an earlier case of this match takes", goType)
		}
		seen[goType] = true
		return goType
	}
	for _, cl := range s.Clauses {
		a := arm{body: cl.Body, def: len(cl.List) == 0}
		for _, item := range cl.List {
			p := fc.l.info.Patterns[item]
			if p == nil {
				ice(item, "a case with no Patterns entry")
			}
			switch p.Kind {
			case check.PatWildcard:
				a.def = true
			case check.PatTypeTest:
				a.types = append(a.types, caseType(p.Type, item))
			case check.PatAs:
				if len(cl.List) != 1 || len(p.Elems) != 1 || p.Elems[0].Kind != check.PatTypeTest || p.Var == nil {
					unreachable(item, ruleSimpleMatch, "an as pattern that is not one type test in a case of its own")
				}
				a.types = append(a.types, caseType(p.Elems[0].Type, item))
				a.as = p.Var
				bind = true
			default:
				unreachable(item, ruleSimpleMatch, "a pattern that is not a type test in a type switch")
			}
		}
		arms = append(arms, a)
	}
	head := "switch " + subject + ".(type) {"
	narrowed := ""
	if bind {
		narrowed = fc.temp("v")
		head = "switch " + narrowed + " := " + subject + ".(type) {"
	}
	fc.line(head)
	fc.enterSwitch()
	// Go: a switch with a default whose every arm terminates is terminating.
	terminates, hasDefault := true, false
	for _, a := range arms {
		hasDefault = hasDefault || a.def
		if a.def {
			fc.line("default:")
		} else {
			fc.line("case " + strings.Join(a.types, ", ") + ":")
		}
		fc.depth++
		if a.as != nil {
			name := fc.declare(a.as)
			fc.line(name + " := " + narrowed)
			if !fc.l.reads[a.as] {
				fc.line("_ = " + name)
			}
		} else if bind {
			fc.line("_ = " + narrowed)
		}
		terminates = fc.yieldList(a.body, d) && terminates
		fc.depth--
	}
	if !hasDefault && d.needsDefault(terminates) {
		hasDefault = true
		fc.line("default:")
		if bind {
			fc.line("\t_ = " + narrowed)
		}
		fc.noCase(s)
	}
	fc.leaveSwitch()
	fc.line("}")
	return terminates && hasDefault
}

// valueMatch lowers a match on constant patterns to a Go expression switch,
// or a match with no subject to an if chain.
func (fc *funcCtx) valueMatch(s *syntax.SwitchStmt, d dest) bool {
	if fc.l.info.Matches[s] == nil {
		ice(s, "a value match with no Matches entry")
	}
	if s.Tag == nil {
		ice(s, "a match with no subject; the checker refuses it as undecided syntax")
	}
	tag := fc.headerValue(s.Tag)
	fc.line("switch " + tag + " {")
	fc.enterSwitch()
	terminates, hasDefault := true, false
	seen := map[string]bool{}
	for _, cl := range s.Clauses {
		var values []string
		def := false
		for _, item := range cl.List {
			p := fc.l.info.Patterns[item]
			if p == nil {
				ice(item, "a value case with no Patterns entry")
			}
			switch p.Kind {
			case check.PatWildcard:
				def = true
			case check.PatConst:
				key := caseKey(p.Type, p.Value)
				if seen[key] {
					unreachable(item, ruleCoveredCase, "a case value that an earlier case of this match takes")
				}
				seen[key] = true
				values = append(values, fc.constant(p.Type, p.Value, item))
			default:
				unreachable(item, ruleLiteralCases, "a value match case that is not a literal")
			}
		}
		if def || len(cl.List) == 0 {
			hasDefault = true
			fc.line("default:")
		} else {
			fc.line("case " + strings.Join(values, ", ") + ":")
		}
		fc.depth++
		terminates = fc.yieldList(cl.Body, d) && terminates
		fc.depth--
	}
	if !hasDefault && d.needsDefault(terminates) {
		hasDefault = true
		fc.line("default:")
		fc.noCase(s)
	}
	fc.leaveSwitch()
	fc.line("}")
	return terminates && hasDefault
}

// caseKey is a case constant's value in its type, as Go compares switch
// cases: a float rounded to its width, so two literals that round to one
// float32 are one value.
func caseKey(t check.Type, v constant.Value) string {
	if b, ok := t.(*check.Basic); ok {
		switch b.Kind {
		case check.Float32:
			f, _ := constant.Float32Val(constant.ToFloat(v))
			return "f32:" + strconv.FormatFloat(float64(f), 'g', -1, 32)
		case check.Float64:
			f, _ := constant.Float64Val(constant.ToFloat(v))
			return "f64:" + strconv.FormatFloat(f, 'g', -1, 64)
		}
	}
	if isInteger(t) {
		if i := constant.ToInt(v); i.Kind() == constant.Int {
			return "int:" + i.ExactString()
		}
	}
	return v.Kind().String() + ":" + v.ExactString()
}

// isInterface reports a type that lowers to a Go interface: a union or an
// operation type.
func (fc *funcCtx) isInterface(t check.Type) bool {
	if _, row := t.(*check.Union); row {
		return true // a row value, a Go error
	}
	n, ok := t.(*check.Named)
	if !ok || fc.l.runtimeType(n, fc.site) != "" {
		return false // a std type is the runtime's concrete Go type
	}
	td := fc.l.typeDecl(n.Obj, fc.site)
	if td.kind == kindDefined {
		return fc.isInterface(td.defined)
	}
	return td.kind == kindUnion || td.kind == kindOps
}
