package gobind

import (
	"go/ast"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// The flow proof (GOBIND-FLOW). A function's body runs forward over a
// state of facts: the local variables that are non-nil, the pairs
// (x, err) of one proven (*T, error) call's results, of which one is
// non-nil, and the field paths `x.f` an `x.f != nil` proved, until the
// next call or assignment. Each return reads the state where it stands.
//
//   - Conditions refine: `x != nil` proves x where it holds, `x == nil`
//     proves x where it fails and, for a pair (x, err), err == nil
//     proves x. &&, || and ! combine; a branch the facts contradict is
//     dead.
//   - Assignments set: a variable assigned a non-nil pointer or a
//     non-nil error value is non-nil; `x, err := g(…)` with g proven
//     makes a pair; any other assignment forgets.
//   - A dereference proves: after `x.f` (a field, not a method value of
//     a pointer method) or `*x` evaluates, x is non-nil, since a nil x
//     panics there.
//   - Branches join by intersection; a loop runs to a fixed point; a
//     break or a continue carries its state to its target; a range over
//     a slice, map or string `len(x) == 0` ruled out leaves only after
//     its body ran (GOBIND-LOOP). A goto's label knows nothing.
//   - Names resolve by scope: each declaration is its own variable.
//
// A variable whose address is taken loses its facts at every call. One a
// function literal assigns loses them where a function literal is
// written or a local variable holding one is called, when the flow sees
// every run of that literal (it is called where it is written, deferred,
// or held by variables used only by direct calls); a literal that
// escapes (passed, stored, returned, started with go) makes what it
// assigns unknown in the whole function (escapes, GOBIND-CLOSURE-SOUND).
// A parameter cannot hold a closure of this call.
//
// A deferred function runs after the return: one that may recover (a
// literal or a listed function calling recover) returns whatever the
// results hold when the panic came, so the function proves nothing; a
// deferred literal may assign a named result only a non-nil value.

// nnState is what the flow knows at one point.
type nnState struct {
	dead  bool               // no path reaches here
	nn    map[string]bool    // variable ids and field paths ("@…") non-nil
	pairs map[[2]string]bool // (x, err): one of the two is non-nil
}

func newState() nnState {
	return nnState{nn: map[string]bool{}, pairs: map[[2]string]bool{}}
}

func deadState() nnState { return nnState{dead: true} }

func (s nnState) copy() nnState {
	if s.dead {
		return s
	}
	return nnState{nn: maps.Clone(s.nn), pairs: maps.Clone(s.pairs)}
}

// nnJoin is what holds on both a and b.
func nnJoin(a, b nnState) nnState {
	switch {
	case a.dead:
		return b.copy()
	case b.dead:
		return a.copy()
	}
	out := newState()
	for k := range a.nn {
		if b.nn[k] {
			out.nn[k] = true
		}
	}
	// A pair holds on a side that holds it or knows one of the two
	// non-nil: `if err != nil { err = fmt.Errorf(…) }` keeps (x, err).
	for _, s := range []nnState{a, b} {
		for k := range s.pairs {
			if a.implies(k) && b.implies(k) {
				out.pairs[k] = true
			}
		}
	}
	return out
}

func (s nnState) implies(k [2]string) bool {
	return s.pairs[k] || s.nn[k[0]] || s.nn[k[1]]
}

func (s nnState) equal(t nnState) bool {
	if s.dead || t.dead {
		return s.dead == t.dead
	}
	return maps.Equal(s.nn, t.nn) && maps.Equal(s.pairs, t.pairs)
}

// forget drops what s knows of the variable id.
func (s *nnState) forget(id string) {
	if s.dead {
		return
	}
	delete(s.nn, id)
	delete(s.nn, id+nnNonEmpty)
	for k := range s.pairs {
		if k[0] == id || k[1] == id {
			delete(s.pairs, k)
		}
	}
}

// forgetPaths drops the field paths: a call or an assignment may write a
// field.
func (s *nnState) forgetPaths() {
	if s.dead {
		return
	}
	for k := range s.nn {
		if strings.HasPrefix(k, "@") {
			delete(s.nn, k)
		}
	}
}

// isNil is s where key is nil: the other of each pair is non-nil, and a
// key s knows non-nil makes the branch dead.
func (s nnState) isNil(key string) nnState {
	if s.dead {
		return s
	}
	if s.nn[key] {
		return deadState()
	}
	out := s.copy()
	for k := range s.pairs {
		switch key {
		case k[0]:
			out.nn[k[1]] = true
		case k[1]:
			out.nn[k[0]] = true
		}
	}
	return out
}

func (s nnState) nonNil(key string) nnState {
	if s.dead {
		return s
	}
	out := s.copy()
	out.nn[key] = true
	return out
}

// nnResolver gives each local declaration of a function an id, name#n,
// and each identifier that names a local variable its id.
type nnResolver struct {
	ids   map[*ast.Ident]string
	owner map[string]*ast.FuncLit // the literal that declares an id, nil for the function
	lits  []*ast.FuncLit
	stack []map[string]string
	n     int
}

func resolveLocals(fd *ast.FuncDecl) (map[*ast.Ident]string, map[string]*ast.FuncLit) {
	r := &nnResolver{ids: map[*ast.Ident]string{}, owner: map[string]*ast.FuncLit{}}
	r.push()
	r.fields(fd.Recv)
	r.fields(fd.Type.Params)
	r.fields(fd.Type.Results)
	for _, s := range fd.Body.List {
		r.stmt(s)
	}
	return r.ids, r.owner
}

func (r *nnResolver) push() { r.stack = append(r.stack, map[string]string{}) }
func (r *nnResolver) pop()  { r.stack = r.stack[:len(r.stack)-1] }

func (r *nnResolver) declare(id *ast.Ident) {
	if id == nil || id.Name == "_" {
		return
	}
	r.n++
	s := id.Name + "#" + strconv.Itoa(r.n)
	r.stack[len(r.stack)-1][id.Name] = s
	r.ids[id] = s
	if len(r.lits) > 0 {
		r.owner[s] = r.lits[len(r.lits)-1]
	}
}

func (r *nnResolver) use(id *ast.Ident) {
	for i := len(r.stack) - 1; i >= 0; i-- {
		if s, ok := r.stack[i][id.Name]; ok {
			r.ids[id] = s
			return
		}
	}
}

func (r *nnResolver) fields(fl *ast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		r.expr(f.Type)
	}
	for _, f := range fl.List {
		for _, n := range f.Names {
			r.declare(n)
		}
	}
}

func (r *nnResolver) expr(x ast.Node) {
	if x == nil {
		return
	}
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			r.use(n)
		case *ast.SelectorExpr:
			r.expr(n.X)
			return false
		case *ast.FuncLit:
			r.lits = append(r.lits, n)
			r.push()
			r.fields(n.Type.Params)
			r.fields(n.Type.Results)
			for _, s := range n.Body.List {
				r.stmt(s)
			}
			r.pop()
			r.lits = r.lits[:len(r.lits)-1]
			return false
		}
		return true
	})
}

func (r *nnResolver) stmts(list []ast.Stmt) {
	for _, s := range list {
		r.stmt(s)
	}
}

func (r *nnResolver) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case nil:
	case *ast.BlockStmt:
		r.push()
		r.stmts(s.List)
		r.pop()
	case *ast.AssignStmt:
		for _, x := range s.Rhs {
			r.expr(x)
		}
		for _, l := range s.Lhs {
			id, ok := l.(*ast.Ident)
			if s.Tok != token.DEFINE || !ok {
				r.expr(l)
				continue
			}
			if prev, again := r.stack[len(r.stack)-1][id.Name]; again {
				r.ids[id] = prev // a redeclaration assigns
			} else {
				r.declare(id)
			}
		}
	case *ast.DeclStmt:
		g, ok := s.Decl.(*ast.GenDecl)
		if !ok {
			return
		}
		for _, sp := range g.Specs {
			switch sp := sp.(type) {
			case *ast.ValueSpec:
				r.expr(sp.Type)
				for _, v := range sp.Values {
					r.expr(v)
				}
				for _, n := range sp.Names {
					r.declare(n)
				}
			case *ast.TypeSpec:
				r.declare(sp.Name)
				r.expr(sp.Type)
			}
		}
	case *ast.IfStmt:
		r.push()
		r.stmt(s.Init)
		r.expr(s.Cond)
		r.stmt(s.Body)
		r.stmt(s.Else)
		r.pop()
	case *ast.ForStmt:
		r.push()
		r.stmt(s.Init)
		r.expr(s.Cond)
		r.stmt(s.Post)
		r.stmt(s.Body)
		r.pop()
	case *ast.RangeStmt:
		r.expr(s.X)
		r.push()
		for _, x := range []ast.Expr{s.Key, s.Value} {
			if x == nil {
				continue
			}
			if id, ok := x.(*ast.Ident); ok && s.Tok == token.DEFINE {
				r.declare(id)
			} else {
				r.expr(x)
			}
		}
		r.stmt(s.Body)
		r.pop()
	case *ast.SwitchStmt:
		r.push()
		r.stmt(s.Init)
		r.expr(s.Tag)
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			for _, x := range cc.List {
				r.expr(x)
			}
			r.push()
			r.stmts(cc.Body)
			r.pop()
		}
		r.pop()
	case *ast.TypeSwitchStmt:
		r.push()
		r.stmt(s.Init)
		if a, ok := s.Assign.(*ast.AssignStmt); ok && len(a.Lhs) == 1 {
			for _, x := range a.Rhs {
				r.expr(x)
			}
			if id, ok := a.Lhs[0].(*ast.Ident); ok {
				r.declare(id)
			}
		} else {
			r.stmt(s.Assign)
		}
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			for _, x := range cc.List {
				r.expr(x)
			}
			r.push()
			r.stmts(cc.Body)
			r.pop()
		}
		r.pop()
	case *ast.SelectStmt:
		for _, c := range s.Body.List {
			cc := c.(*ast.CommClause)
			r.push()
			r.stmt(cc.Comm)
			r.stmts(cc.Body)
			r.pop()
		}
	case *ast.LabeledStmt:
		r.stmt(s.Stmt)
	case *ast.ReturnStmt:
		for _, x := range s.Results {
			r.expr(x)
		}
	case *ast.ExprStmt:
		r.expr(s.X)
	case *ast.SendStmt:
		r.expr(s.Chan)
		r.expr(s.Value)
	case *ast.IncDecStmt:
		r.expr(s.X)
	case *ast.GoStmt:
		r.expr(s.Call)
	case *ast.DeferStmt:
		r.expr(s.Call)
	}
}

// nnTarget is a loop, switch or select a break (a loop a continue) may
// leave to.
type nnTarget struct {
	label        string
	loop         bool
	breaks, cont []nnState
}

// nnFlow runs one function's body.
type nnFlow struct {
	p        *nnProof
	ok       bool
	results  []string        // the named results' ids
	addr     map[string]bool // ids whose address is taken
	captured map[string]bool // ids a function literal assigns
	closures map[string]bool // ids assigned a function literal
	gotos    map[string]bool // the labels a goto names
	targets  []*nnTarget
	// collections are the ids whose declared type is a slice, an array,
	// a map or a string: a range over one known non-empty runs its body
	// at least once (GOBIND-LOOP). A channel's length may drop under
	// another receiver, so a channel is none.
	collections map[string]bool
}

// nnNonEmpty suffixes a collection's id in nnState.nn: `len(x) == 0`
// failed, and x was not assigned since.
const nnNonEmpty = "[]"

// collections are the parameters and var-declared locals of fd whose
// written type is a slice, an array, a map or a string.
func collections(p *nnProof, fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	is := func(t ast.Expr) bool {
		switch t := t.(type) {
		case *ast.Ellipsis, *ast.ArrayType, *ast.MapType:
			return true
		case *ast.Ident:
			return t.Name == "string" && t.Obj == nil
		}
		return false
	}
	add := func(names []*ast.Ident, t ast.Expr) {
		if !is(t) {
			return
		}
		for _, n := range names {
			if id := p.ids[n]; id != "" {
				out[id] = true
			}
		}
	}
	for _, fl := range fd.Type.Params.List {
		add(fl.Names, fl.Type)
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if vs, ok := n.(*ast.ValueSpec); ok && vs.Type != nil {
			add(vs.Names, vs.Type)
		}
		return true
	})
	return out
}

// lenCompared is the id x of a comparison of len(x) with 0 and the
// operator with len(x) on the left (`0 < len(x)` is `len(x) > 0`), or "".
func (f *nnFlow) lenCompared(c *ast.BinaryExpr) (string, token.Token) {
	op := c.Op
	x, y := ast.Unparen(c.X), ast.Unparen(c.Y)
	if isZero(x) {
		x, y = y, x
		switch op {
		case token.LSS:
			op = token.GTR
		case token.GTR:
			op = token.LSS
		}
	}
	call, ok := x.(*ast.CallExpr)
	if !ok || !isZero(y) || len(call.Args) != 1 || !isIdentNamed(call.Fun, "len") {
		return "", op
	}
	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); !ok || f.p.ids[id] != "" {
		return "", op // a local named len
	}
	id := f.p.id(call.Args[0])
	if !f.collections[id] {
		return "", op
	}
	return id, op
}

func isZero(x ast.Expr) bool {
	l, ok := x.(*ast.BasicLit)
	return ok && l.Kind == token.INT && l.Value == "0"
}

// gotoLabels are the labels the gotos of body (outside function
// literals) name.
func gotoLabels(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(c ast.Node) bool {
		switch c := c.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if c.Tok == token.GOTO && c.Label != nil {
				out[c.Label.Name] = true
			}
		}
		return true
	})
	return out
}

// body proves every return of the function outside its function
// literals.
func (p *nnProof) body() bool {
	fd := p.fn.fd
	var owner map[string]*ast.FuncLit
	p.ids, owner = resolveLocals(fd)
	f := &nnFlow{p: p, ok: true, addr: map[string]bool{}, captured: map[string]bool{}, closures: map[string]bool{}, gotos: gotoLabels(fd.Body)}
	if fd.Type.Results != nil {
		for _, fl := range fd.Type.Results.List {
			for _, n := range fl.Names {
				f.results = append(f.results, p.ids[n])
			}
		}
	}
	f.scan(owner)
	f.escapes(owner)
	if !f.ok {
		return false
	}
	f.collections = collections(p, fd)
	f.list(fd.Body.List, newState())
	return f.ok
}

// id is the local variable x names, or "".
func (p *nnProof) id(x ast.Expr) string {
	if id, ok := ast.Unparen(x).(*ast.Ident); ok {
		return p.ids[id]
	}
	return ""
}

// scan finds the addressed, captured and closure variables and checks
// the defers.
func (f *nnFlow) scan(owner map[string]*ast.FuncLit) {
	p := f.p
	var lits []*ast.FuncLit
	inner := func() *ast.FuncLit {
		if len(lits) == 0 {
			return nil
		}
		return lits[len(lits)-1]
	}
	write := func(x ast.Expr) {
		if id := p.id(x); id != "" && owner[id] != inner() {
			f.captured[id] = true
		}
	}
	var walk func(n ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(c ast.Node) bool {
			switch c := c.(type) {
			case *ast.FuncLit:
				lits = append(lits, c)
				walk(c.Body)
				lits = lits[:len(lits)-1]
				return false
			case *ast.AssignStmt:
				for i, l := range c.Lhs {
					write(l)
					if len(c.Lhs) == len(c.Rhs) {
						if _, lit := ast.Unparen(c.Rhs[i]).(*ast.FuncLit); lit {
							if id := p.id(l); id != "" {
								f.closures[id] = true
							}
						}
					}
				}
			case *ast.ValueSpec:
				for i, n := range c.Names {
					if i < len(c.Values) {
						if _, lit := ast.Unparen(c.Values[i]).(*ast.FuncLit); lit {
							f.closures[p.ids[n]] = true
						}
					}
				}
			case *ast.IncDecStmt:
				write(c.X)
			case *ast.RangeStmt:
				if c.Tok == token.ASSIGN {
					write(c.Key)
					write(c.Value)
				}
			case *ast.UnaryExpr:
				if c.Op == token.AND {
					if id := p.id(c.X); id != "" {
						f.addr[id] = true
					}
				}
			case *ast.DeferStmt:
				if inner() == nil {
					f.deferred(c)
				}
			}
			return true
		})
	}
	walk(p.fn.fd.Body)
	for _, r := range f.results {
		if f.addr[r] && hasDefer(p.fn.fd.Body) {
			f.ok = false // a deferred call may write it through its address
		}
	}
}

// escapes makes a variable a function literal assigns unknown for the
// whole function where the flow cannot see each run of that literal
// (GOBIND-CLOSURE-SOUND). The flow sees a literal called where it is
// written (`func() { … }()`, or deferred: the defer rule judges it), or
// held in a local variable whose every use is a direct call; a literal
// passed, stored, returned, started with go, or held by a variable used
// otherwise (`k := g`, `run(g)`, `defer g()`) escapes, and so does every
// literal inside one that escapes. Each mention of an unknown variable
// gets an id of its own, so no fact reaches from one mention to
// another; an escaping literal that assigns a named result leaves the
// function unproven, since it may run after the return.
func (f *nnFlow) escapes(owner map[string]*ast.FuncLit) {
	p := f.p
	type use struct {
		id   string
		in   *ast.FuncLit
		call bool
	}
	parent := map[*ast.FuncLit]*ast.FuncLit{}
	esc := map[*ast.FuncLit]bool{}
	held := map[*ast.FuncLit][]string{}
	writes := map[*ast.FuncLit][]string{}
	var uses []use
	var stack []ast.Node
	var lits []*ast.FuncLit
	at := func(i int) ast.Node { // the i-th node above the current one
		if len(stack) < i {
			return nil
		}
		return stack[len(stack)-i]
	}
	ast.Inspect(p.fn.fd.Body, func(n ast.Node) bool {
		if n == nil {
			if _, ok := stack[len(stack)-1].(*ast.FuncLit); ok {
				lits = lits[:len(lits)-1]
			}
			stack = stack[:len(stack)-1]
			return true
		}
		var in *ast.FuncLit
		if len(lits) > 0 {
			in = lits[len(lits)-1]
		}
		write := func(x ast.Expr) {
			if id := p.id(x); id != "" && in != nil && owner[id] != in {
				writes[in] = append(writes[in], id)
			}
		}
		switch n := n.(type) {
		case *ast.FuncLit:
			parent[n] = in
			esc[n] = true
			switch up := at(1).(type) {
			case *ast.CallExpr:
				if up.Fun == n {
					_, goes := at(2).(*ast.GoStmt)
					esc[n] = goes
				}
			case *ast.AssignStmt:
				for i, r := range up.Rhs {
					if r == n && len(up.Lhs) == len(up.Rhs) {
						if id := p.id(up.Lhs[i]); id != "" {
							esc[n] = false
							held[n] = append(held[n], id)
						}
					}
				}
			case *ast.ValueSpec:
				for i, v := range up.Values {
					if v == n && i < len(up.Names) && len(up.Names) == len(up.Values) {
						if id := p.ids[up.Names[i]]; id != "" {
							esc[n] = false
							held[n] = append(held[n], id)
						}
					}
				}
			}
			lits = append(lits, n)
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				write(l)
			}
		case *ast.IncDecStmt:
			write(n.X)
		case *ast.RangeStmt:
			if n.Tok == token.ASSIGN {
				write(n.Key)
				write(n.Value)
			}
		case *ast.Ident:
			id := p.ids[n]
			if id == "" || !f.closures[id] {
				break
			}
			switch up := at(1).(type) {
			case *ast.AssignStmt:
				if slices.Contains(up.Lhs, ast.Expr(n)) {
					stack = append(stack, n)
					return true // a write, not a use
				}
			case *ast.ValueSpec:
				if slices.Contains(up.Names, n) {
					stack = append(stack, n)
					return true
				}
			case *ast.CallExpr:
				if up.Fun == n {
					switch at(2).(type) {
					case *ast.GoStmt, *ast.DeferStmt:
					default:
						uses = append(uses, use{id, in, true})
						stack = append(stack, n)
						return true
					}
				}
			}
			uses = append(uses, use{id, in, false})
		}
		stack = append(stack, n)
		return true
	})
	varEsc := map[string]bool{}
	for _, u := range uses {
		if !u.call {
			varEsc[u.id] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for l := range parent {
			if esc[l] {
				continue
			}
			if parent[l] != nil && esc[parent[l]] || slices.ContainsFunc(held[l], func(id string) bool { return varEsc[id] }) {
				esc[l], changed = true, true
			}
		}
		for _, u := range uses {
			if !varEsc[u.id] && u.in != nil && esc[u.in] {
				varEsc[u.id], changed = true, true
			}
		}
	}
	unknown := map[string]bool{}
	for l, ids := range writes {
		if esc[l] {
			for _, id := range ids {
				unknown[id] = true
			}
		}
	}
	for _, r := range f.results {
		if unknown[r] {
			f.ok = false
		}
	}
	n := 0
	for ident, id := range p.ids {
		if unknown[id] {
			n++
			fresh := id + "!" + strconv.Itoa(n)
			p.ids[ident] = fresh
			f.closures[fresh] = f.closures[id]
		}
	}
}

func hasDefer(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		switch c.(type) {
		case *ast.FuncLit:
			return false
		case *ast.DeferStmt:
			found = true
		}
		return !found
	})
	return found
}

// recovers reports a direct call of recover in body.
func recovers(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(c ast.Node) bool {
		switch c := c.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			found = found || isIdentNamed(c.Fun, "recover")
		}
		return !found
	})
	return found
}

// deferred checks one defer of the function.
func (f *nnFlow) deferred(d *ast.DeferStmt) {
	p := f.p
	lit, ok := ast.Unparen(d.Call.Fun).(*ast.FuncLit)
	if !ok {
		if fn := p.w.funcs[p.callee(d.Call)]; fn != nil && fn.fd.Body != nil && recovers(fn.fd.Body) {
			f.ok = false
		}
		return
	}
	if recovers(lit.Body) {
		f.ok = false
		return
	}
	result := map[string]bool{}
	for _, r := range f.results {
		result[r] = r != "" // a result named _ is written only by a return
	}
	// Where the error result is nil, the return gave a non-nil pointer,
	// so the literal may set the error to anything there:
	// `if err == nil { err = cerr }` (multipart.Reader.ReadForm).
	errID := ""
	if p.fn.kind == nnPtrErr && len(f.results) == 2 {
		errID = f.results[1]
	}
	empty := newState()
	var walk func(n ast.Node, errNil bool)
	walk = func(n ast.Node, errNil bool) {
		ast.Inspect(n, func(c ast.Node) bool {
			switch c := c.(type) {
			case *ast.IfStmt:
				if c.Init != nil {
					walk(c.Init, errNil)
				}
				walk(c.Body, errNil || f.provesNil(c.Cond, errID))
				if c.Else != nil {
					walk(c.Else, errNil)
				}
				return false
			case *ast.AssignStmt:
				for i, l := range c.Lhs {
					id := p.id(l)
					if !result[id] || errNil && id == errID {
						continue
					}
					if c.Tok != token.ASSIGN || len(c.Lhs) != len(c.Rhs) || !f.value(c.Rhs[i], empty) {
						f.ok = false
					}
				}
			case *ast.IncDecStmt:
				if result[p.id(c.X)] {
					f.ok = false
				}
			}
			return true
		})
	}
	walk(lit.Body, false)
}

// provesNil reports c true only where the variable id is nil: `id ==
// nil`, or an && holding it.
func (f *nnFlow) provesNil(c ast.Expr, id string) bool {
	b, ok := ast.Unparen(c).(*ast.BinaryExpr)
	if !ok || id == "" {
		return false
	}
	switch b.Op {
	case token.LAND:
		return f.provesNil(b.X, id) || f.provesNil(b.Y, id)
	case token.EQL:
		return f.nilCompared(b) == id
	}
	return false
}

// value reports x a non-nil value to store in a variable: a non-nil
// pointer or a non-nil error value. A concrete value's typed nil is no
// such value: the variable may be a pointer.
func (f *nnFlow) value(x ast.Expr, st nnState) bool {
	p := f.p
	if p.pointer(x, st) || errorValue(x, p.fn.imports) {
		return true
	}
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		return p.errorName(x, st)
	case *ast.SelectorExpr:
		return p.errorName(x, st)
	case *ast.CallExpr:
		return p.provenCall(x, nnErr)
	}
	return false
}

// effects is st after x evaluates: what it dereferences is non-nil, and
// what its calls may write is forgotten.
func (f *nnFlow) effects(x ast.Node, st nnState) nnState {
	if x == nil || st.dead {
		return st
	}
	st = st.copy()
	var derefs []string
	f.derefs(x, &derefs)
	for _, id := range derefs {
		st.nn[id] = true
	}
	f.kills(x, &st)
	return st
}

// derefs are the variables x surely dereferences: the right operand of
// && and || may not run.
func (f *nnFlow) derefs(x ast.Node, out *[]string) {
	p := f.p
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BinaryExpr:
			if n.Op == token.LAND || n.Op == token.LOR {
				f.derefs(n.X, out)
				return false
			}
		case *ast.CallExpr:
			// A method call's receiver is evaluated, not dereferenced: a
			// pointer method takes a nil receiver.
			if sel, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr); ok {
				f.derefs(sel.X, out)
			} else {
				f.derefs(n.Fun, out)
			}
			for _, a := range n.Args {
				f.derefs(a, out)
			}
			return false
		case *ast.SelectorExpr:
			if id := p.id(n.X); id != "" && !p.w.ptrMethods[n.Sel.Name] {
				*out = append(*out, id)
			}
		case *ast.StarExpr:
			if id := p.id(n.X); id != "" {
				*out = append(*out, id)
			}
		}
		return true
	})
}

// kills forgets what x's calls may write: field paths, addressed
// variables, and at a function literal or a local closure's call the
// captured ones.
func (f *nnFlow) kills(x ast.Node, st *nnState) {
	p := f.p
	call, closure := false, false
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			closure = true
			return false
		case *ast.CallExpr:
			call = true
			if f.closures[p.id(n.Fun)] {
				closure = true
			}
		case *ast.UnaryExpr:
			call = call || n.Op == token.ARROW
		}
		return true
	})
	if call || closure {
		st.forgetPaths()
	}
	if call {
		for id := range f.addr {
			st.forget(id)
		}
	}
	if closure {
		for id := range f.captured {
			st.forget(id)
		}
	}
}

func (f *nnFlow) list(list []ast.Stmt, st nnState) nnState {
	for _, s := range list {
		if l, ok := s.(*ast.LabeledStmt); ok && f.gotos[l.Label.Name] {
			// A goto may come from anywhere: nothing is known here. Go
			// jumps over no declaration into its scope, so forgetting
			// is enough.
			st = newState()
		}
		if st.dead {
			continue
		}
		st = f.stmt(s, "", st)
	}
	return st
}

func (f *nnFlow) stmt(s ast.Stmt, label string, st nnState) nnState {
	p := f.p
	switch s := s.(type) {
	case nil, *ast.EmptyStmt:
		return st
	case *ast.BlockStmt:
		return f.list(s.List, st)
	case *ast.LabeledStmt:
		return f.stmt(s.Stmt, s.Label.Name, st)
	case *ast.ExprStmt:
		st = f.effects(s.X, st)
		if call, ok := s.X.(*ast.CallExpr); ok && isIdentNamed(call.Fun, "panic") && p.ids[call.Fun.(*ast.Ident)] == "" {
			return deadState()
		}
		return st
	case *ast.SendStmt:
		st = f.effects(s.Chan, st)
		st = f.effects(s.Value, st)
		st.forgetPaths()
		return st
	case *ast.IncDecStmt:
		st = f.effects(s.X, st)
		st.forgetPaths()
		st.forget(p.id(s.X))
		return st
	case *ast.GoStmt:
		return f.callArgs(s.Call, st)
	case *ast.DeferStmt:
		return f.callArgs(s.Call, st)
	case *ast.AssignStmt:
		return f.assign(s.Lhs, s.Rhs, s.Tok, st)
	case *ast.DeclStmt:
		g, ok := s.Decl.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			return st
		}
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			lhs := make([]ast.Expr, len(vs.Names))
			for i, n := range vs.Names {
				lhs[i] = n
			}
			st = f.assign(lhs, vs.Values, token.DEFINE, st)
		}
		return st
	case *ast.ReturnStmt:
		f.ret(s, st)
		return deadState()
	case *ast.IfStmt:
		st = f.stmt(s.Init, "", st)
		t, e := f.cond(s.Cond, st)
		t = f.list(s.Body.List, t)
		if s.Else != nil {
			e = f.stmt(s.Else, "", e)
		}
		return nnJoin(t, e)
	case *ast.ForStmt:
		st = f.stmt(s.Init, "", st)
		return f.loop(label, st, func(head nnState, t *nnTarget) (exit, back nnState) {
			body, exit := head, deadState()
			if s.Cond != nil {
				body, exit = f.cond(s.Cond, head)
			}
			end := f.list(s.Body.List, body)
			for _, c := range t.cont {
				end = nnJoin(end, c)
			}
			return exit, f.stmt(s.Post, "", end)
		})
	case *ast.RangeStmt:
		st = f.effects(s.X, st)
		// A range over a collection known non-empty leaves only after a
		// body has run: its exit is what the body brings back.
		once := st.nn[p.id(s.X)+nnNonEmpty] && p.id(s.X) != ""
		return f.loop(label, st, func(head nnState, t *nnTarget) (exit, back nnState) {
			body := head.copy()
			body.forgetPaths() // a range assigns, and may receive
			for _, x := range []ast.Expr{s.Key, s.Value} {
				if x != nil {
					body = f.effects(x, body)
					body.forget(p.id(x))
				}
			}
			end := f.list(s.Body.List, body)
			for _, c := range t.cont {
				end = nnJoin(end, c)
			}
			if once {
				return end, end
			}
			return head, end
		})
	case *ast.SwitchStmt:
		return f.switchStmt(s, label, st)
	case *ast.TypeSwitchStmt:
		st = f.stmt(s.Init, "", st)
		v := ""
		switch a := s.Assign.(type) {
		case *ast.AssignStmt:
			for _, x := range a.Rhs {
				st = f.effects(x, st)
			}
			v = p.id(a.Lhs[0])
		case *ast.ExprStmt:
			st = f.effects(a.X, st)
		}
		t := &nnTarget{label: label}
		f.targets = append(f.targets, t)
		out, hasDefault := deadState(), false
		for _, c := range s.Body.List {
			cc := c.(*ast.CaseClause)
			hasDefault = hasDefault || cc.List == nil
			entry := st.copy()
			entry.forget(v)
			out = nnJoin(out, f.list(cc.Body, entry))
		}
		f.targets = f.targets[:len(f.targets)-1]
		if !hasDefault {
			out = nnJoin(out, st)
		}
		for _, b := range t.breaks {
			out = nnJoin(out, b)
		}
		return out
	case *ast.SelectStmt:
		t := &nnTarget{label: label}
		f.targets = append(f.targets, t)
		out := deadState()
		for _, c := range s.Body.List {
			cc := c.(*ast.CommClause)
			entry := f.stmt(cc.Comm, "", st)
			entry.forgetPaths()
			out = nnJoin(out, f.list(cc.Body, entry))
		}
		f.targets = f.targets[:len(f.targets)-1]
		for _, b := range t.breaks {
			out = nnJoin(out, b)
		}
		return out
	case *ast.BranchStmt:
		switch s.Tok {
		case token.BREAK, token.CONTINUE:
			t := f.target(s)
			if t == nil {
				f.ok = false
			} else if s.Tok == token.BREAK {
				t.breaks = append(t.breaks, st)
			} else {
				t.cont = append(t.cont, st)
			}
			return deadState()
		case token.FALLTHROUGH:
			return st // the switch reads it
		case token.GOTO:
			return deadState() // its label forgets (list)
		}
		f.ok = false
		return deadState()
	}
	f.ok = false // a statement the flow does not know
	return deadState()
}

// target is the loop, switch or select a break or continue leaves.
func (f *nnFlow) target(s *ast.BranchStmt) *nnTarget {
	for i := len(f.targets) - 1; i >= 0; i-- {
		t := f.targets[i]
		switch {
		case s.Label != nil:
			if t.label == s.Label.Name {
				return t
			}
		case s.Tok == token.BREAK || t.loop:
			return t
		}
	}
	return nil
}

// loop runs a loop's body to a fixed point: step gives the state that
// leaves at the head and the one that comes back to it.
func (f *nnFlow) loop(label string, entry nnState, step func(head nnState, t *nnTarget) (exit, back nnState)) nnState {
	head := entry
	for range 64 {
		t := &nnTarget{label: label, loop: true}
		f.targets = append(f.targets, t)
		exit, back := step(head, t)
		f.targets = f.targets[:len(f.targets)-1]
		next := nnJoin(entry, back)
		if next.equal(head) {
			for _, b := range t.breaks {
				exit = nnJoin(exit, b)
			}
			return exit
		}
		head = next
	}
	f.ok = false
	return deadState()
}

// switchStmt runs an expression switch: a tagless one's cases refine.
func (f *nnFlow) switchStmt(s *ast.SwitchStmt, label string, st nnState) nnState {
	st = f.stmt(s.Init, "", st)
	st = f.effects(s.Tag, st)
	entries := make([]nnState, len(s.Body.List))
	rest, def := st, -1
	for i, c := range s.Body.List {
		cc := c.(*ast.CaseClause)
		if cc.List == nil {
			def = i
			continue
		}
		matched := deadState()
		for _, x := range cc.List {
			var yes nnState
			if s.Tag == nil {
				yes, rest = f.cond(x, rest)
			} else {
				rest = f.effects(x, rest)
				yes = rest
			}
			matched = nnJoin(matched, yes)
		}
		entries[i] = matched
	}
	out := deadState()
	if def >= 0 {
		entries[def] = rest
	} else {
		out = rest
	}
	t := &nnTarget{label: label}
	f.targets = append(f.targets, t)
	through := deadState()
	for i, c := range s.Body.List {
		cc := c.(*ast.CaseClause)
		end := f.list(cc.Body, nnJoin(entries[i], through))
		through = deadState()
		if n := len(cc.Body); n > 0 {
			if b, ok := cc.Body[n-1].(*ast.BranchStmt); ok && b.Tok == token.FALLTHROUGH {
				through = end
				continue
			}
		}
		out = nnJoin(out, end)
	}
	f.targets = f.targets[:len(f.targets)-1]
	for _, b := range t.breaks {
		out = nnJoin(out, b)
	}
	return out
}

// callArgs is st after a go or defer statement evaluates its function
// and arguments; the call itself runs later.
func (f *nnFlow) callArgs(call *ast.CallExpr, st nnState) nnState {
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok {
		st = f.effects(sel.X, st)
	} else if _, lit := call.Fun.(*ast.FuncLit); !lit {
		st = f.effects(call.Fun, st)
	}
	for _, a := range call.Args {
		st = f.effects(a, st)
	}
	return st
}

// assign is st after lhs tok rhs (or a var declaration, tok DEFINE).
func (f *nnFlow) assign(lhs, rhs []ast.Expr, tok token.Token, st nnState) nnState {
	p := f.p
	for _, x := range rhs {
		st = f.effects(x, st)
	}
	for _, l := range lhs {
		if p.id(l) == "" {
			st = f.effects(l, st)
		}
	}
	st = st.copy()
	st.forgetPaths()
	plain := tok == token.ASSIGN || tok == token.DEFINE
	switch {
	case len(lhs) == len(rhs):
		set := make([]bool, len(lhs))
		for i := range lhs {
			set[i] = plain && f.value(rhs[i], st)
		}
		for i, l := range lhs {
			if id := p.id(l); id != "" {
				st.forget(id)
				if set[i] {
					st.nn[id] = true
				}
			} else if path := p.path(l); path != "" && set[i] && len(lhs) == 1 {
				// x.f = v: x.f holds v until a call or an assignment
				// (alone: x.f, y.f = … may name one field twice).
				st.nn[path] = true
			}
		}
	case len(rhs) == 0: // var x T: the zero value
		for _, l := range lhs {
			st.forget(p.id(l))
		}
	default:
		for _, l := range lhs {
			st.forget(p.id(l))
		}
		if len(lhs) == 2 && len(rhs) == 1 && plain && p.provenCall(rhs[0], nnPtrErr) {
			x, e := p.id(lhs[0]), p.id(lhs[1])
			if x != "" && e != "" && x != e {
				st.pairs[[2]string{x, e}] = true
			}
		}
	}
	return st
}

// cond is the states where c holds and where it fails.
func (f *nnFlow) cond(c ast.Expr, st nnState) (yes, no nnState) {
	if st.dead {
		return st, st
	}
	switch c := ast.Unparen(c).(type) {
	case *ast.UnaryExpr:
		if c.Op == token.NOT {
			yes, no = f.cond(c.X, st)
			return no, yes
		}
	case *ast.BinaryExpr:
		switch c.Op {
		case token.LAND:
			xy, xn := f.cond(c.X, st)
			yy, yn := f.cond(c.Y, xy)
			return yy, nnJoin(xn, yn)
		case token.LOR:
			xy, xn := f.cond(c.X, st)
			yy, yn := f.cond(c.Y, xn)
			return nnJoin(xy, yy), yn
		case token.GTR, token.LSS:
			st = f.effects(c, st)
			if id, op := f.lenCompared(c); id != "" && op == token.GTR {
				return st.nonNil(id + nnNonEmpty), st
			}
			return st, st
		case token.EQL, token.NEQ:
			st = f.effects(c, st)
			if id, _ := f.lenCompared(c); id != "" {
				if c.Op == token.NEQ {
					return st.nonNil(id + nnNonEmpty), st
				}
				return st, st.nonNil(id + nnNonEmpty)
			}
			key := f.nilCompared(c)
			if key == "" {
				return st, st
			}
			if c.Op == token.NEQ {
				return st.nonNil(key), st.isNil(key)
			}
			return st.isNil(key), st.nonNil(key)
		}
	}
	st = f.effects(c, st)
	return st, st
}

// nilCompared is the key of what c compares with nil: a local variable's
// id, or a field path "@…", or "".
func (f *nnFlow) nilCompared(c *ast.BinaryExpr) string {
	x, y := ast.Unparen(c.X), ast.Unparen(c.Y)
	if f.isNil(x) {
		x, y = y, x
	}
	if !f.isNil(y) {
		return ""
	}
	if id := f.p.id(x); id != "" {
		return id
	}
	return f.p.path(x)
}

func (f *nnFlow) isNil(x ast.Expr) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == "nil" && f.p.ids[id] == ""
}

// path is the key "@base.f.g" of a field path x.f.g, its base a local
// variable's id or a package name, or "".
func (p *nnProof) path(x ast.Expr) string {
	var sels []string
	for {
		switch e := ast.Unparen(x).(type) {
		case *ast.SelectorExpr:
			sels = append(sels, e.Sel.Name)
			x = e.X
			continue
		case *ast.Ident:
			if len(sels) == 0 {
				return ""
			}
			base := p.ids[e]
			if base == "" {
				base = "pkg:" + e.Name
			}
			for i := len(sels) - 1; i >= 0; i-- {
				base += "." + sels[i]
			}
			return "@" + base
		}
		return ""
	}
}

// ret proves one return statement where st stands.
func (f *nnFlow) ret(r *ast.ReturnStmt, st nnState) {
	p := f.p
	results := r.Results
	if len(results) == 0 {
		for _, id := range f.results {
			results = append(results, ast.NewIdent(id)) // a bare return: an id, not a name
		}
		if len(results) == 0 {
			f.ok = false
			return
		}
	}
	for _, x := range results {
		st = f.effects(x, st)
	}
	bare := len(r.Results) == 0
	ptr := func(i int) bool {
		if bare {
			return st.nn[results[i].(*ast.Ident).Name]
		}
		return p.pointer(results[i], st)
	}
	errSet := func(i int) bool {
		if bare {
			return st.nn[results[i].(*ast.Ident).Name]
		}
		return p.errorSet(results[i], st)
	}
	key := func(i int) string {
		if bare {
			return results[i].(*ast.Ident).Name
		}
		return p.id(results[i])
	}
	var ok bool
	switch p.fn.kind {
	case nnPtr:
		ok = len(results) == 1 && ptr(0)
	case nnErr:
		ok = len(results) == 1 && errSet(0)
	case nnPtrErr:
		switch len(results) {
		case 1:
			ok = !bare && p.provenCall(results[0], nnPtrErr)
		case 2:
			ok = ptr(0) || errSet(1) || st.pairs[[2]string{key(0), key(1)}]
		}
	}
	if !ok {
		f.ok = false
	}
}
