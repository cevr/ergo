package proto

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Diagnostic codes.
const (
	codeUndeclaredError = "S101" // an error can escape but is not in the declared row
	codeMissingCap      = "S102" // a capability is required but not declared or provided
	codeCatchNotTotal   = "S103" // a catch without try misses an error
	codeUnknownName     = "S104"
	codeUnhandled       = "S105" // a fallible call whose failure is neither tried, caught nor returned
	codeBadProvider     = "S106" // a context provider does not implement its capability
	codeMissingReturn   = "S107"
	codeType            = "S108" // type mismatch or misuse
	codeUnsupported     = "S109" // outside the prototype's subset
	codeUnused          = "S110" // a variable is never read, as in Go
)

// reserved names would collide with names the emitter generates or with Go.
var reserved = map[string]bool{
	"rt": true, "time": true, "fmt": true, "init": true, "Error": true,
	// The prototype's intrinsic package and type names.
	"timeout": true, "duration": true,
	// Go's predeclared identifiers: a source binding would shadow them in the output.
	"any": true, "append": true, "bool": true, "byte": true, "cap": true, "clear": true,
	"close": true, "comparable": true, "complex": true, "complex64": true, "complex128": true,
	"copy": true, "delete": true, "error": true, "false": true, "float32": true, "float64": true,
	"imag": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"iota": true, "len": true, "make": true, "max": true, "min": true, "new": true, "nil": true,
	"panic": true, "print": true, "println": true, "real": true, "recover": true, "rune": true,
	"string": true, "true": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
}

func (c *checker) checkLocalName(pos Pos, name string) {
	if reserved[name] || strings.HasSuffix(name, "_") || strings.Contains(name, "__") {
		c.errorf(pos, codeUnsupported, "%s is reserved by the prototype's Go output", name)
	}
}

const expiredType = "timeout.Expired"

// info is what the checker learns about one expression.
type info struct {
	typ      string   // "" when the expression has no value
	fallible bool     // the expression can fail and its failure is not yet handled
	errs     []string // its error row when fallible, sorted
	konst    bool     // an int64 constant expression, whose value is cval
	cval     int64
}

// Checked is the checker's output, read by the emitter.
type Checked struct {
	File       *File
	Types      map[string]*TypeDecl
	Funcs      map[string]*FuncDecl
	Methods    map[string]map[string]*FuncDecl
	Contexts   map[string]*ContextDecl
	ErrorTypes map[string]bool
	Info       map[Expr]info
	Afters     map[*Call]bool // the timeout.After calls, as the checker resolved them
	Caps       []string       // every capability type, sorted: the emitted env fields
}

type checker struct {
	*Checked
	diags []Diagnostic
}

// prelude declares the host capabilities and providers this subset knows.
func prelude() []*TypeDecl {
	return []*TypeDecl{
		{Name: "Console", Kind: kindCapability, Methods: []*Sig{{Name: "Println"}}},
		// Sleep waits a duration on the scheduler, or until the task is stopped. It
		// has no error row: an interruption is not a typed failure.
		{Name: "Scheduler", Kind: kindCapability, Methods: []*Sig{{Name: "Sleep", Params: []Param{{Name: "d", Type: "duration"}}}}},
		{Name: "LiveScheduler", Kind: kindStruct},
	}
}

var hostProvided = map[string]bool{"Console": true}

var basicTypes = map[string]bool{"string": true, "int64": true, "bool": true}

// Check checks a parsed file. It returns the checked program and every
// diagnostic, sorted by position.
func Check(f *File) (*Checked, []Diagnostic) {
	c := &checker{Checked: &Checked{
		File:       f,
		Types:      map[string]*TypeDecl{},
		Funcs:      map[string]*FuncDecl{},
		Methods:    map[string]map[string]*FuncDecl{},
		Contexts:   map[string]*ContextDecl{},
		ErrorTypes: map[string]bool{expiredType: true},
		Info:       map[Expr]info{},
		Afters:     map[*Call]bool{},
	}}
	c.declare()
	for _, fn := range f.Funcs {
		c.funcBody(fn)
	}
	for _, w := range f.Contexts {
		// A top-level context is built by its own function at every with that names
		// it, so its providers must be closed: they require no capability from the
		// caller and let no failure escape. They may still use capabilities they
		// provide to themselves and handle their own failures.
		fc := &fnCtx{escapes: []map[string]Pos{{}}, needs: map[string]Pos{}, scopes: []map[string]*local{{}}}
		c.contextEntries(w.Entries, fc)
		for _, u := range sortedKeys(fc.needs) {
			c.errorf(fc.needs[u], codeMissingCap, "wire %s requires %s to build a provider; build it in an inline wire instead", w.Name, u)
		}
		for _, e := range sortedKeys(fc.escapes[0]) {
			c.errorf(fc.escapes[0][e], codeUndeclaredError, "wire %s can fail with %s while building a provider; build it in an inline wire instead", w.Name, e)
		}
		if c.contextReaches(w.Name, w.Name, map[string]bool{}) {
			c.errorf(w.Pos, codeUnsupported, "wire %s needs itself to build its providers", w.Name)
		}
	}
	sort.SliceStable(c.diags, func(i, j int) bool {
		a, b := c.diags[i].Pos, c.diags[j].Pos
		return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col
	})
	return c.Checked, c.diags
}

func (c *checker) errorf(pos Pos, code, format string, args ...any) {
	c.diags = append(c.diags, Diagnostic{pos, code, fmt.Sprintf(format, args...)})
}

func (c *checker) declare() {
	f := c.File
	for _, d := range prelude() {
		c.Types[d.Name] = d
	}
	for _, d := range f.Types {
		c.checkLocalName(d.Pos, d.Name)
		if c.Types[d.Name] != nil {
			c.errorf(d.Pos, codeType, "%s is declared twice", d.Name)
		}
		c.Types[d.Name] = d
	}
	for _, w := range f.Contexts {
		c.checkLocalName(w.Pos, w.Name)
		if c.Contexts[w.Name] != nil {
			c.errorf(w.Pos, codeType, "wire %s is declared twice", w.Name)
		}
		c.Contexts[w.Name] = w
	}
	for _, fn := range f.Funcs {
		// Go puts types and functions in one package namespace.
		if fn.Recv == nil {
			if fn.Name != "main" {
				c.checkLocalName(fn.Pos, fn.Name)
			}
			if c.Funcs[fn.Name] != nil || c.Types[fn.Name] != nil {
				c.errorf(fn.Pos, codeType, "%s is declared twice", fn.Name)
			}
			c.Funcs[fn.Name] = fn
			continue
		}
		c.checkLocalName(fn.Pos, fn.Name)
		if c.Methods[fn.Recv.Type] == nil {
			c.Methods[fn.Recv.Type] = map[string]*FuncDecl{}
		}
		if c.Methods[fn.Recv.Type][fn.Name] != nil {
			c.errorf(fn.Pos, codeType, "%s.%s is declared twice", fn.Recv.Type, fn.Name)
		}
		if d := c.Types[fn.Recv.Type]; d != nil {
			for _, fd := range d.Fields {
				if fd.Name == fn.Name {
					c.errorf(fn.Pos, codeType, "%s has both a field and a method named %s", d.Name, fn.Name)
				}
			}
		}
		c.Methods[fn.Recv.Type][fn.Name] = fn
	}
	for _, fn := range f.Funcs {
		c.checkParams(&fn.Sig, fn.Recv)
	}
	if m := c.Funcs["main"]; m == nil {
		c.errorf(Pos{1, 1}, codeUnsupported, "a program needs func main")
	} else if len(m.Params) > 0 {
		c.errorf(m.Pos, codeUnsupported, "main takes no parameters")
	}
	for name, d := range c.Types {
		if d.Kind == kindCapability {
			c.Caps = append(c.Caps, name)
		}
	}
	sort.Strings(c.Caps)
	for _, d := range f.Types {
		for _, fd := range d.Fields {
			// Fields keep their source names, so they follow the generated-name
			// rule; Go's predeclared names are fine as fields.
			if fd.Name == "Error" || strings.HasSuffix(fd.Name, "_") || strings.Contains(fd.Name, "__") {
				c.errorf(fd.Pos, codeUnsupported, "field %s is reserved by the prototype's Go output", fd.Name)
			}
		}
	}
	// Every type named in an error row is an error type.
	sigs := []*Sig{}
	for _, fn := range f.Funcs {
		sigs = append(sigs, &fn.Sig)
	}
	for _, d := range f.Types {
		sigs = append(sigs, d.Methods...)
		methods := map[string]bool{}
		for _, m := range d.Methods {
			c.checkLocalName(m.Pos, m.Name)
			if methods[m.Name] {
				c.errorf(m.Pos, codeType, "%s.%s is declared twice", d.Name, m.Name)
			}
			methods[m.Name] = true
			c.checkParams(m, nil)
			// M5 (round 3): a provider method gets only the task, so a capability
			// method cannot carry its own requirements.
			if len(m.Needs) > 0 {
				c.errorf(m.Pos, codeUnsupported, "a capability method with a needs clause is outside this prototype")
			}
		}
		fields := map[string]bool{}
		for _, fd := range d.Fields {
			c.checkTypeName(fd.Pos, fd.Type)
			if fields[fd.Name] {
				c.errorf(fd.Pos, codeType, "%s has two fields named %s", d.Name, fd.Name)
			}
			fields[fd.Name] = true
		}
		variants := map[string]bool{}
		for _, v := range d.Variants {
			// M5 (round 2): the emitter writes a marker method on each variant, so
			// it must be a struct declared in this file.
			if !c.local(v) || c.Types[v].Kind != kindStruct {
				c.errorf(d.Pos, codeType, "union variant %s must be a struct type declared in this file", v)
			}
			if variants[v] {
				c.errorf(d.Pos, codeType, "%s lists %s twice", d.Name, v)
			}
			variants[v] = true
		}
		if d.Kind == kindStruct && c.layoutCycle(d.Name, map[string]int{}) {
			c.errorf(d.Pos, codeType, "%s contains itself by value; put the recursion behind a union", d.Name)
		}
	}
	for _, s := range sigs {
		for i, q := range s.Errors {
			name := c.resolveError(q)
			s.Errors[i] = QName{Pos: q.Pos, Name: name}
			if name != "" {
				c.ErrorTypes[name] = true
			}
		}
		for _, q := range s.Needs {
			if d := c.Types[q.String()]; d == nil || d.Kind != kindCapability {
				c.errorf(q.Pos, codeUnknownName, "unknown capability %s", q)
			}
		}
		for _, p := range s.Params {
			c.checkTypeName(p.Pos, p.Type)
		}
		if s.Result != "" {
			c.checkTypeName(s.Pos, s.Result)
		}
	}
}

// contextReaches reports whether building context from reaches with(target),
// directly or through the functions its initializers call.
func (c *checker) contextReaches(from, target string, seen map[string]bool) bool {
	w := c.Contexts[from]
	if w == nil {
		return false
	}
	var exprs []Expr
	for _, en := range w.Entries {
		exprs = append(exprs, en.Value)
	}
	return c.reaches(exprs, nil, target, seen)
}

// reaches walks expressions and statements for a with(target), following
// called functions and named contexts once each.
func (c *checker) reaches(exprs []Expr, stmts []Stmt, target string, seen map[string]bool) bool {
	for _, s := range stmts {
		switch s := s.(type) {
		case *DefineStmt:
			exprs = append(exprs, s.Value)
		case *ReturnStmt:
			if s.Value != nil {
				exprs = append(exprs, s.Value)
			}
		case *ExprStmt:
			exprs = append(exprs, s.X)
		case *IfStmt:
			exprs = append(exprs, s.Cond)
			stmts := append([]Stmt{}, s.Then.Stmts...)
			if s.Else != nil {
				stmts = append(stmts, s.Else.Stmts...)
			}
			if c.reaches(nil, stmts, target, seen) {
				return true
			}
		case *TypeSwitchStmt:
			exprs = append(exprs, s.Subject)
			for _, cs := range s.Cases {
				if c.reaches(nil, cs.Body.Stmts, target, seen) {
					return true
				}
			}
		}
	}
	for _, e := range exprs {
		switch e := e.(type) {
		case *Selector:
			if c.reaches([]Expr{e.X}, nil, target, seen) {
				return true
			}
		case *Binary:
			if c.reaches([]Expr{e.L, e.R}, nil, target, seen) {
				return true
			}
		case *CompositeLit:
			for _, el := range e.Elems {
				if c.reaches([]Expr{el.Value}, nil, target, seen) {
					return true
				}
			}
		case *TryExpr:
			if c.reaches([]Expr{e.X}, nil, target, seen) {
				return true
			}
		case *CatchExpr:
			sub := []Expr{e.X}
			for _, a := range e.Arms {
				sub = append(sub, a.Value)
			}
			if c.reaches(sub, nil, target, seen) {
				return true
			}
		case *ContextLit:
			for _, en := range e.Entries {
				if c.reaches([]Expr{en.Value}, nil, target, seen) {
					return true
				}
			}
		case *Call:
			if id, ok := e.Fun.(*Ident); ok {
				if id.Name == "with" && len(e.Args) == 1 {
					if w, ok := e.Args[0].(*Ident); ok {
						if w.Name == target {
							return true
						}
						if !seen["wire "+w.Name] {
							seen["wire "+w.Name] = true
							if c.contextReaches(w.Name, target, seen) {
								return true
							}
						}
					}
				} else if fn := c.Funcs[id.Name]; fn != nil && !seen[id.Name] {
					seen[id.Name] = true
					if c.reaches(nil, fn.Body.Stmts, target, seen) {
						return true
					}
				}
			}
			// M4 (round 3): a capability call runs whichever provider is hasContexts, so
			// follow every method of that name. This over-approximates: it may
			// reject a context that only an unrelated provider would cycle through.
			if sel, ok := e.Fun.(*Selector); ok {
				if id, ok := sel.X.(*Ident); ok {
					if d := c.Types[id.Name]; d != nil && d.Kind == kindCapability {
						for typ, ms := range c.Methods {
							key := "method " + typ + "." + sel.Sel
							if m := ms[sel.Sel]; m != nil && !seen[key] {
								seen[key] = true
								if c.reaches(nil, m.Body.Stmts, target, seen) {
									return true
								}
							}
						}
					}
				}
			}
			sub := append([]Expr{}, e.Args...)
			if c.reaches(sub, nil, target, seen) {
				return true
			}
			if e.Block != nil && c.reaches(nil, e.Block.Stmts, target, seen) {
				return true
			}
		}
	}
	return false
}

// checkParams checks parameter and receiver names in one signature scope.
func (c *checker) checkParams(s *Sig, recv *Param) {
	names := map[string]bool{}
	if recv != nil && recv.Name != "" {
		names[recv.Name] = true
	}
	for _, p := range s.Params {
		c.checkLocalName(p.Pos, p.Name)
		if names[p.Name] {
			c.errorf(p.Pos, codeType, "%s is declared twice in this signature", p.Name)
		}
		names[p.Name] = true
	}
}

// intLiteral decodes a decimal int64 literal. A leading zero is rejected:
// Go would read it as octal.
func (c *checker) intLiteral(pos Pos, text string) (int64, bool) {
	if len(text) > 1 && text[0] == '0' {
		c.errorf(pos, codeType, "integer literal %s has a leading zero", text)
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		c.errorf(pos, codeType, "integer literal %s overflows int64", text)
		return 0, false
	}
	return n, true
}

var durationUnits = map[string]int64{
	"ns": 1, "us": 1e3, "ms": 1e6, "s": 1e9, "m": 60e9, "h": 3600e9,
}

// durationLiteral checks that a duration literal fits in int64 nanoseconds.
func (c *checker) durationLiteral(pos Pos, text string) {
	i := strings.IndexFunc(text, func(r rune) bool { return r < '0' || r > '9' })
	n, ok := c.intLiteral(pos, text[:i])
	if unit := durationUnits[text[i:]]; ok && n > (1<<63-1)/unit {
		c.errorf(pos, codeType, "duration %s overflows int64 nanoseconds", text)
	}
}

// local reports whether a type is declared in the source file, not the prelude.
func (c *checker) local(t string) bool {
	for _, d := range c.File.Types {
		if d.Name == t {
			return true
		}
	}
	return false
}

// layoutCycle reports whether struct t reaches itself through by-value struct
// fields. A union or capability field is an interface in Go, so it breaks the cycle.
// state is 1 while t is on the walk's path and 2 once t is finished, so each
// type is walked once.
func (c *checker) layoutCycle(t string, state map[string]int) bool {
	d := c.Types[t]
	if d == nil || d.Kind != kindStruct || state[t] == 2 {
		return false
	}
	if state[t] == 1 {
		return true
	}
	state[t] = 1
	for _, f := range d.Fields {
		if c.layoutCycle(f.Type, state) {
			return true
		}
	}
	state[t] = 2
	return false
}

func (c *checker) checkTypeName(pos Pos, t string) {
	if !basicTypes[t] && c.Types[t] == nil {
		c.errorf(pos, codeUnknownName, "unknown type %s", t)
	}
}

func (c *checker) resolveError(q QName) string {
	if q.Pkg != "" {
		if q.String() == expiredType && slices.Contains(c.File.Imports, "timeout") {
			return expiredType
		}
		c.errorf(q.Pos, codeUnknownName, "unknown error type %s", q)
		return ""
	}
	if d := c.Types[q.Name]; d == nil || d.Kind != kindStruct || !c.local(q.Name) {
		c.errorf(q.Pos, codeUnknownName, "unknown error type %s: an error must be a struct declared in this file", q.Name)
		return ""
	}
	return q.Name
}

// fnCtx tracks one function body.
type fnCtx struct {
	sig      *Sig
	method   bool
	escapes  []map[string]Pos // the innermost failure target: the function or a timeout block
	needs    map[string]Pos
	provided []map[string]bool
	scopes   []map[string]*local
	unused   func(*local)
	blocks   int // depth of with and timeout.After blocks
}

// local is a variable in scope. Parameters start used; Go rejects any other
// variable that is never read, so the sketch does too.
type local struct {
	pos  Pos
	name string
	typ  string
	used bool
}

func (fc *fnCtx) lookup(name string) (string, bool) {
	for i := len(fc.scopes) - 1; i >= 0; i-- {
		if l, ok := fc.scopes[i][name]; ok {
			l.used = true
			return l.typ, true
		}
	}
	return "", false
}

func (fc *fnCtx) bind(l *local) { fc.scopes[len(fc.scopes)-1][l.name] = l }

func (fc *fnCtx) push() { fc.scopes = append(fc.scopes, map[string]*local{}) }
func (fc *fnCtx) pop() {
	for _, l := range fc.scopes[len(fc.scopes)-1] {
		if !l.used && fc.unused != nil {
			fc.unused(l)
		}
	}
	fc.scopes = fc.scopes[:len(fc.scopes)-1]
}

func (fc *fnCtx) escape(pos Pos, errs []string) {
	top := fc.escapes[len(fc.escapes)-1]
	for _, e := range errs {
		if _, ok := top[e]; !ok {
			top[e] = pos
		}
	}
}

func (fc *fnCtx) use(pos Pos, cap string) {
	for _, p := range fc.provided {
		if p[cap] {
			return
		}
	}
	if _, ok := fc.needs[cap]; !ok {
		fc.needs[cap] = pos
	}
}

func (c *checker) funcBody(fn *FuncDecl) {
	fc := &fnCtx{sig: &fn.Sig, method: fn.Recv != nil, escapes: []map[string]Pos{{}}, needs: map[string]Pos{}}
	fc.unused = func(l *local) { c.errorf(l.pos, codeUnused, "%s declared and not used", l.name) }
	fc.push()
	if fn.Recv != nil {
		if d := c.Types[fn.Recv.Type]; d == nil || d.Kind != kindStruct || !c.local(fn.Recv.Type) {
			c.errorf(fn.Recv.Pos, codeType, "a method receiver must be a struct type declared in this file")
		}
		if fn.Recv.Name != "" {
			c.checkLocalName(fn.Recv.Pos, fn.Recv.Name)
			fc.bind(&local{pos: fn.Recv.Pos, name: fn.Recv.Name, typ: fn.Recv.Type, used: true})
		}
		if len(fn.Needs) > 0 {
			c.errorf(fn.Pos, codeUnsupported, "methods with a needs clause are outside this prototype")
		}
	}
	for _, p := range fn.Params {
		fc.bind(&local{pos: p.Pos, name: p.Name, typ: p.Type, used: true})
	}
	c.stmts(fn.Body.Stmts, fc)
	fc.pop()
	if fn.Result != "" && !c.terminates(fn.Body) {
		c.errorf(fn.Pos, codeMissingReturn, "%s can reach its end without returning a %s", fn.Name, fn.Result)
	}
	declared := map[string]bool{}
	for _, q := range fn.Errors {
		declared[q.Name] = true
	}
	for _, e := range sortedKeys(fc.escapes[0]) {
		if !declared[e] {
			c.errorf(fc.escapes[0][e], codeUndeclaredError, "%s can fail with %s, which its error row does not declare", fn.Name, e)
		}
	}
	allowed := map[string]bool{}
	for _, q := range fn.Needs {
		allowed[q.String()] = true
	}
	for _, u := range sortedKeys(fc.needs) {
		if !allowed[u] {
			c.errorf(fc.needs[u], codeMissingCap, "%s requires %s, which its needs clause does not list and no with provides", fn.Name, u)
		}
	}
	if fn.Name == "main" && fn.Recv == nil {
		for _, q := range fn.Needs {
			if !hostProvided[q.String()] {
				c.errorf(q.Pos, codeMissingCap, "main requires %s, which the host does not provide", q)
			}
		}
		if fn.Result != "" || fn.Fallible {
			c.errorf(fn.Pos, codeUnsupported, "main must return nothing in this prototype")
		}
	}
}

func sortedKeys(m map[string]Pos) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *checker) stmts(list []Stmt, fc *fnCtx) {
	for _, s := range list {
		c.stmt(s, fc)
	}
}

func (c *checker) stmt(s Stmt, fc *fnCtx) {
	switch s := s.(type) {
	case *DefineStmt:
		v := c.expr(s.Value, fc)
		if v.fallible {
			c.errorf(s.Pos, codeUnhandled, "the failure of this call is not handled: use try, catch or return")
		}
		if v.typ == "" {
			c.errorf(s.Pos, codeType, "%s := needs a value", s.Name)
		}
		if _, dup := fc.scopes[len(fc.scopes)-1][s.Name]; dup {
			c.errorf(s.Pos, codeType, "%s is already declared in this block", s.Name)
		}
		c.checkLocalName(s.Pos, s.Name)
		fc.bind(&local{pos: s.Pos, name: s.Name, typ: v.typ})
	case *ReturnStmt:
		c.ret(s, fc)
	case *IfStmt:
		if v := c.value(s.Cond, fc); v.typ != "bool" {
			c.errorf(s.Pos, codeType, "an if condition must be bool, not %s", v.typ)
		}
		c.scoped(s.Then, fc)
		if s.Else != nil {
			c.scoped(s.Else, fc)
		}
	case *TypeSwitchStmt:
		v := c.value(s.Subject, fc)
		d := c.Types[v.typ]
		if d == nil || d.Kind != kindUnion {
			c.errorf(s.Pos, codeType, "a type switch needs a union value, not %s", v.typ)
			return
		}
		// One binding shared by every case: Go rejects it only when no case reads it.
		shared := &local{pos: s.Pos, name: s.Bind}
		c.checkLocalName(s.Pos, s.Bind)
		seen := map[string]bool{}
		for _, cs := range s.Cases {
			bind := v.typ
			for _, q := range cs.Types {
				if !slices.Contains(d.Variants, q.String()) {
					c.errorf(q.Pos, codeType, "%s is not a variant of %s", q, v.typ)
				}
				if seen[q.String()] {
					c.errorf(q.Pos, codeType, "case %s appears twice", q)
				}
				seen[q.String()] = true
			}
			if len(cs.Types) == 1 {
				bind = cs.Types[0].String()
			}
			fc.push()
			l := &local{pos: s.Pos, name: s.Bind, typ: bind, used: true}
			fc.bind(l)
			l.used = false
			c.stmts(cs.Body.Stmts, fc)
			shared.used = shared.used || l.used
			l.used = true
			fc.pop()
		}
		if !shared.used {
			c.errorf(s.Pos, codeUnused, "%s declared and not used", s.Bind)
		}
	case *ExprStmt:
		switch s.X.(type) {
		case *Call, *TryExpr, *CatchExpr:
		default:
			c.errorf(s.Pos, codeUnused, "this expression's value is not used")
		}
		if v := c.expr(s.X, fc); v.fallible {
			c.errorf(s.Pos, codeUnhandled, "the failure of this call is not handled: use try, catch or return")
		}
	}
}

func (c *checker) scoped(b *Block, fc *fnCtx) {
	fc.push()
	c.stmts(b.Stmts, fc)
	fc.pop()
}

func (c *checker) ret(s *ReturnStmt, fc *fnCtx) {
	if fc.blocks > 0 {
		c.errorf(s.Pos, codeUnsupported, "return is not allowed inside a block; a block yields its last expression")
		return
	}
	sig := fc.sig
	if s.Value == nil {
		if sig.Result != "" {
			c.errorf(s.Pos, codeType, "missing return value of type %s", sig.Result)
		}
		return
	}
	v := c.expr(s.Value, fc)
	switch {
	case v.fallible:
		// return f() passes f's success and failure through.
		fc.escape(s.Pos, v.errs)
		if !c.assignable(v.typ, sig.Result) {
			c.errorf(s.Pos, codeType, "cannot return %s as %s", v.typ, sig.Result)
		}
	case c.ErrorTypes[v.typ]:
		fc.escape(s.Pos, []string{v.typ})
	case sig.Result == "":
		c.errorf(s.Pos, codeType, "%s returns no value", sig.Name)
	case !c.assignable(v.typ, sig.Result):
		c.errorf(s.Pos, codeType, "cannot return %s as %s", v.typ, sig.Result)
	}
}

func (c *checker) assignable(from, to string) bool {
	if from == to {
		return true
	}
	if d := c.Types[to]; d != nil && d.Kind == kindUnion {
		return slices.Contains(d.Variants, from)
	}
	return false
}

func (c *checker) terminates(b *Block) bool {
	if len(b.Stmts) == 0 {
		return false
	}
	switch s := b.Stmts[len(b.Stmts)-1].(type) {
	case *ReturnStmt:
		return true
	case *IfStmt:
		return s.Else != nil && c.terminates(s.Then) && c.terminates(s.Else)
	case *TypeSwitchStmt:
		covered := map[string]bool{}
		for _, cs := range s.Cases {
			if !c.terminates(cs.Body) {
				return false
			}
			for _, q := range cs.Types {
				covered[q.String()] = true
			}
		}
		info := c.Info[s.Subject]
		d := c.Types[info.typ]
		if d == nil {
			return false
		}
		for _, v := range d.Variants {
			if !covered[v] {
				return false
			}
		}
		return true
	}
	return false
}

func (c *checker) expr(e Expr, fc *fnCtx) info {
	v := c.expr1(e, fc)
	sort.Strings(v.errs)
	v.errs = slices.Compact(v.errs)
	c.Info[e] = v
	return v
}

func (c *checker) expr1(e Expr, fc *fnCtx) info {
	switch e := e.(type) {
	case *BasicLit:
		switch e.Kind {
		case tString:
			// M5 (round 4): decode once with Go's rules, so the emitted spelling
			// is one Go accepts.
			if _, err := strconv.Unquote(e.Text); err != nil {
				c.errorf(e.Pos, codeType, "invalid string literal %s", e.Text)
			}
			return info{typ: "string"}
		case tDuration:
			c.durationLiteral(e.Pos, e.Text)
			return info{typ: "duration"}
		}
		// M7 (round 2): Go folds constant arithmetic at compile time and rejects
		// overflow, so the checker does too.
		n, ok := c.intLiteral(e.Pos, e.Text)
		return info{typ: "int64", konst: ok, cval: n}
	case *Ident:
		if t, ok := fc.lookup(e.Name); ok {
			return info{typ: t}
		}
		c.errorf(e.Pos, codeUnknownName, "unknown name %s", e.Name)
		return info{typ: "?"}
	case *Selector:
		if id, ok := e.X.(*Ident); ok {
			if d := c.Types[id.Name]; d != nil && d.Kind == kindCapability {
				c.errorf(e.Pos, codeType, "%s.%s must be called", id.Name, e.Sel)
				return info{typ: "?"}
			}
		}
		x := c.expr(e.X, fc)
		if x.fallible {
			c.errorf(e.Pos, codeUnhandled, "the failure of this call is not handled")
		}
		if d := c.Types[x.typ]; d != nil && d.Kind == kindStruct {
			for _, f := range d.Fields {
				if f.Name == e.Sel {
					return info{typ: f.Type}
				}
			}
		}
		if x.typ != "?" {
			c.errorf(e.Pos, codeUnknownName, "%s has no field %s", x.typ, e.Sel)
		}
		return info{typ: "?"}
	case *Binary:
		l, r := c.expr(e.L, fc), c.expr(e.R, fc)
		if l.fallible || r.fallible {
			c.errorf(e.Pos, codeUnhandled, "the failure of an operand is not handled")
		}
		switch e.Op {
		case "==":
			if l.typ != r.typ && l.typ != "?" && r.typ != "?" {
				c.errorf(e.Pos, codeType, "cannot compare %s with %s", l.typ, r.typ)
			}
			if !basicTypes[l.typ] && l.typ != "?" {
				c.errorf(e.Pos, codeUnsupported, "== on %s is outside this prototype", l.typ)
			}
			return info{typ: "bool"}
		case "+":
			if l.typ != r.typ && l.typ != "?" && r.typ != "?" {
				c.errorf(e.Pos, codeType, "mismatched operands %s and %s", l.typ, r.typ)
			}
			if l.typ != "string" && l.typ != "int64" && l.typ != "?" {
				c.errorf(e.Pos, codeType, "+ needs string or int64, not %s", l.typ)
			}
			if l.konst && r.konst {
				sum := l.cval + r.cval
				if (l.cval > 0 && r.cval > 0 && sum < 0) || (l.cval < 0 && r.cval < 0 && sum >= 0) {
					c.errorf(e.Pos, codeType, "constant %d + %d overflows int64", l.cval, r.cval)
					return info{typ: l.typ}
				}
				return info{typ: l.typ, konst: true, cval: sum}
			}
			return info{typ: l.typ}
		}
		// Short-circuit operators would need control flow around hoisted calls.
		c.errorf(e.Pos, codeUnsupported, "operator %s is outside this prototype", e.Op)
		return info{typ: "?"}
	case *CompositeLit:
		return c.composite(e, fc)
	case *Call:
		return c.call(e, fc)
	case *TryExpr:
		if ce, ok := e.X.(*CatchExpr); ok {
			v := c.catch(ce, fc, true)
			c.Info[ce] = v
			return v
		}
		x := c.expr(e.X, fc)
		if !x.fallible {
			c.errorf(e.Pos, codeType, "try needs an expression that can fail")
		}
		fc.escape(e.Pos, x.errs)
		return info{typ: x.typ}
	case *CatchExpr:
		return c.catch(e, fc, false)
	case *ContextLit:
		c.errorf(e.Pos, codeType, "a wire literal is only allowed as the argument of with")
		return info{typ: "?"}
	}
	c.errorf(e.exprPos(), codeUnsupported, "unsupported expression")
	return info{typ: "?"}
}

func (c *checker) catch(e *CatchExpr, fc *fnCtx, try bool) info {
	x := c.expr(e.X, fc)
	if !x.fallible {
		c.errorf(e.Pos, codeType, "catch needs an expression that can fail")
		return info{typ: x.typ}
	}
	handled := map[string]bool{}
	for _, arm := range e.Arms {
		for _, q := range arm.Types {
			name := q.String()
			if !slices.Contains(x.errs, name) {
				c.errorf(q.Pos, codeType, "%s is not in the caught row [%s]", name, strings.Join(x.errs, " | "))
			}
			if handled[name] {
				c.errorf(q.Pos, codeType, "%s is caught twice", name)
			}
			handled[name] = true
		}
		v := c.expr(arm.Value, fc)
		if v.fallible {
			c.errorf(arm.Pos, codeUnhandled, "the failure of this arm is not handled")
		}
		if x.typ == "" && v.typ != "" && v.typ != "?" {
			c.errorf(arm.Pos, codeType, "this arm yields %s, but the caught expression has no value", v.typ)
		}
		if x.typ != "" && x.typ != "?" && v.typ != "?" && !c.assignable(v.typ, x.typ) {
			c.errorf(arm.Pos, codeType, "this arm yields %s, but the caught expression yields %s", v.typ, x.typ)
		}
	}
	var rest []string
	for _, err := range x.errs {
		if !handled[err] {
			rest = append(rest, err)
		}
	}
	if try {
		fc.escape(e.Pos, rest)
	} else if len(rest) > 0 {
		c.errorf(e.Pos, codeCatchNotTotal, "catch without try must handle every error; missing %s", strings.Join(rest, ", "))
	}
	return info{typ: x.typ}
}

// value checks an expression whose value is consumed, so its failure must
// already be handled.
func (c *checker) value(e Expr, fc *fnCtx) info {
	v := c.expr(e, fc)
	if v.fallible {
		c.errorf(e.exprPos(), codeUnhandled, "the failure of this call is not handled: use try, catch or return")
	}
	return v
}

// nondefaultable reports whether a type has no valid zero value: a union, a
// capability, or a struct holding one.
func (c *checker) nondefaultable(t string, seen map[string]bool) bool {
	d := c.Types[t]
	if d == nil || seen[t] {
		return false
	}
	switch d.Kind {
	case kindUnion, kindCapability:
		return true
	}
	seen[t] = true
	for _, f := range d.Fields {
		if c.nondefaultable(f.Type, seen) {
			return true
		}
	}
	return false
}

func (c *checker) composite(e *CompositeLit, fc *fnCtx) info {
	d := c.Types[e.Type]
	if d == nil || d.Kind != kindStruct {
		c.errorf(e.Pos, codeUnknownName, "unknown struct type %s", e.Type)
		return info{typ: "?"}
	}
	if e.Positional && len(e.Elems) != len(d.Fields) {
		c.errorf(e.Pos, codeType, "%s has %d fields, not %d", e.Type, len(d.Fields), len(e.Elems))
	}
	if !e.Positional {
		// M12: a field with no valid zero value must be written.
		given := map[string]bool{}
		for _, el := range e.Elems {
			if given[el.Key] {
				c.errorf(e.Pos, codeType, "%s{} sets %s twice", e.Type, el.Key)
			}
			given[el.Key] = true
		}
		for _, f := range d.Fields {
			if !given[f.Name] && c.nondefaultable(f.Type, map[string]bool{}) {
				c.errorf(e.Pos, codeType, "%s{} must set %s: a %s has no zero value", e.Type, f.Name, f.Type)
			}
		}
	}
	for i, el := range e.Elems {
		v := c.value(el.Value, fc)
		want := ""
		if e.Positional {
			if i < len(d.Fields) {
				want = d.Fields[i].Type
			}
		} else {
			for _, f := range d.Fields {
				if f.Name == el.Key {
					want = f.Type
				}
			}
			if want == "" {
				c.errorf(e.Pos, codeUnknownName, "%s has no field %s", e.Type, el.Key)
				continue
			}
		}
		if want != "" && !c.assignable(v.typ, want) && v.typ != "?" {
			c.errorf(e.Pos, codeType, "field of type %s cannot hold %s", want, v.typ)
		}
	}
	return info{typ: e.Type}
}

func (c *checker) call(e *Call, fc *fnCtx) info {
	switch fun := e.Fun.(type) {
	case *Ident:
		if fun.Name == "with" {
			return c.with(e, fc)
		}
		if _, local := fc.lookup(fun.Name); local {
			c.errorf(fun.Pos, codeType, "%s is a variable here, not a function", fun.Name)
			return info{typ: "?"}
		}
		if fun.Name == "main" {
			c.errorf(fun.Pos, codeUnsupported, "main is the program entry and cannot be called")
			return info{typ: "?"}
		}
		fn := c.Funcs[fun.Name]
		if fn == nil {
			c.errorf(fun.Pos, codeUnknownName, "unknown function %s", fun.Name)
			return info{typ: "?"}
		}
		c.args(e, fn.Params, fc)
		for _, q := range fn.Needs {
			fc.use(e.Pos, q.String())
		}
		return sigInfo(&fn.Sig)
	case *Selector:
		id, ok := fun.X.(*Ident)
		if ok && id.Name == "timeout" && fun.Sel == "After" && slices.Contains(c.File.Imports, "timeout") {
			c.Afters[e] = true
			return c.timeoutAfter(e, fc)
		}
		if ok {
			if d := c.Types[id.Name]; d != nil && d.Kind == kindCapability {
				if fc.method {
					c.errorf(e.Pos, codeUnsupported, "provider methods cannot call capabilities in this prototype")
				}
				fc.use(e.Pos, id.Name)
				if id.Name == "Console" && fun.Sel == "Println" {
					for _, a := range e.Args {
						c.argument(a, fc)
					}
					return info{}
				}
				for _, m := range d.Methods {
					if m.Name == fun.Sel {
						c.args(e, m.Params, fc)
						return sigInfo(m)
					}
				}
				c.errorf(fun.Pos, codeUnknownName, "%s has no method %s", id.Name, fun.Sel)
				return info{typ: "?"}
			}
		}
	}
	c.errorf(e.Pos, codeUnsupported, "unsupported call")
	return info{typ: "?"}
}

func sigInfo(s *Sig) info {
	v := info{typ: s.Result, fallible: s.Fallible}
	for _, q := range s.Errors {
		if q.Name != "" {
			v.errs = append(v.errs, q.Name)
		}
	}
	return v
}

// argument checks a call argument: its failure must be handled and it must
// have a value.
func (c *checker) argument(a Expr, fc *fnCtx) info {
	v := c.value(a, fc)
	if v.typ == "" {
		c.errorf(a.exprPos(), codeType, "this argument has no value")
	}
	return v
}

func (c *checker) args(e *Call, params []Param, fc *fnCtx) {
	if len(e.Args) != len(params) {
		c.errorf(e.Pos, codeType, "want %d arguments, got %d", len(params), len(e.Args))
	}
	for i, a := range e.Args {
		v := c.argument(a, fc)
		if i < len(params) && !c.assignable(v.typ, params[i].Type) && v.typ != "?" {
			c.errorf(a.exprPos(), codeType, "argument %d: cannot use %s as %s", i+1, v.typ, params[i].Type)
		}
	}
}

// block checks an expression block and returns the info of its last expression.
func (c *checker) block(b *Block, fc *fnCtx) info {
	fc.blocks++
	defer func() { fc.blocks-- }()
	fc.push()
	defer fc.pop()
	if len(b.Stmts) == 0 {
		c.errorf(b.Pos, codeUnsupported, "a block must end with an expression")
		return info{typ: "?"}
	}
	c.stmts(b.Stmts[:len(b.Stmts)-1], fc)
	last, ok := b.Stmts[len(b.Stmts)-1].(*ExprStmt)
	if !ok {
		c.stmt(b.Stmts[len(b.Stmts)-1], fc)
		c.errorf(b.Stmts[len(b.Stmts)-1].stmtPos(), codeUnsupported, "a block must end with an expression")
		return info{typ: "?"}
	}
	return c.expr(last.X, fc)
}

func (c *checker) with(e *Call, fc *fnCtx) info {
	if len(e.Args) != 1 || e.Block == nil {
		c.errorf(e.Pos, codeType, "with takes one wire and a block")
		return info{typ: "?"}
	}
	var entries []ContextEntry
	switch w := e.Args[0].(type) {
	case *Ident:
		decl := c.Contexts[w.Name]
		if decl == nil {
			c.errorf(w.Pos, codeUnknownName, "unknown wire %s", w.Name)
			return info{typ: "?"}
		}
		entries = decl.Entries
	case *ContextLit:
		entries = w.Entries
		// Inline providers are built here, before the block's discharge, so their
		// requirements and failures belong to the enclosing function.
		c.contextEntries(entries, fc)
	default:
		c.errorf(e.Pos, codeType, "with takes a wire")
		return info{typ: "?"}
	}
	provided := map[string]bool{}
	for _, en := range entries {
		provided[en.Cap] = true
	}
	fc.provided = append(fc.provided, provided)
	v := c.block(e.Block, fc)
	fc.provided = fc.provided[:len(fc.provided)-1]
	return v
}

func (c *checker) timeoutAfter(e *Call, fc *fnCtx) info {
	if len(e.Args) != 1 || e.Block == nil {
		c.errorf(e.Pos, codeType, "timeout.After takes a duration and a block")
		return info{typ: "?"}
	}
	if d := c.value(e.Args[0], fc); d.typ != "duration" {
		c.errorf(e.Pos, codeType, "timeout.After needs a duration, not %s", d.typ)
	}
	fc.use(e.Pos, "Scheduler")
	fc.escapes = append(fc.escapes, map[string]Pos{})
	last := c.block(e.Block, fc)
	inner := fc.escapes[len(fc.escapes)-1]
	fc.escapes = fc.escapes[:len(fc.escapes)-1]
	v := info{typ: last.typ, fallible: true, errs: []string{expiredType}}
	if last.fallible {
		v.errs = append(v.errs, last.errs...)
	}
	v.errs = append(v.errs, sortedKeys(inner)...)
	return v
}

func (c *checker) contextEntries(entries []ContextEntry, fc *fnCtx) {
	for _, en := range entries {
		capDecl := c.Types[en.Cap]
		if capDecl == nil || capDecl.Kind != kindCapability {
			c.errorf(en.Pos, codeUnknownName, "unknown capability %s", en.Cap)
			continue
		}
		lit, ok := en.Value.(*CompositeLit)
		if !ok {
			c.errorf(en.Pos, codeUnsupported, "a wire entry must be a provider literal")
			continue
		}
		c.Info[lit] = c.composite(lit, fc)
		// M9: host capabilities take only their host providers.
		switch {
		case en.Cap == "Scheduler" && lit.Type == "LiveScheduler":
			continue
		case en.Cap == "Scheduler" || en.Cap == "Console" || lit.Type == "LiveScheduler":
			c.errorf(en.Pos, codeBadProvider, "%s cannot provide %s in this prototype", lit.Type, en.Cap)
			continue
		}
		methods := c.Methods[lit.Type]
		for _, m := range capDecl.Methods {
			impl := methods[m.Name]
			if impl == nil {
				c.errorf(en.Pos, codeBadProvider, "%s does not implement %s.%s", lit.Type, en.Cap, m.Name)
				continue
			}
			same := len(impl.Params) == len(m.Params) && impl.Result == m.Result && impl.Fallible == m.Fallible
			for i := 0; same && i < len(m.Params); i++ {
				same = impl.Params[i].Type == m.Params[i].Type
			}
			if !same {
				c.errorf(en.Pos, codeBadProvider, "%s.%s does not match the signature of %s.%s", lit.Type, m.Name, en.Cap, m.Name)
			}
			declared := map[string]bool{}
			for _, q := range m.Errors {
				declared[q.Name] = true
			}
			for _, q := range impl.Errors {
				if !declared[q.Name] {
					c.errorf(en.Pos, codeBadProvider, "%s.%s can fail with %s, which %s.%s does not declare", lit.Type, m.Name, q.Name, en.Cap, m.Name)
				}
			}
		}
	}
}
