package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The built string (MAPCOPY RU12). A let mut string that a loop grows by
// concatenation,
//
//	let mut line = w
//	for w in words {
//		…
//		line = line + " " + w
//		…
//		out = out.Append(line)
//		line = w
//	}
//
// costs Go one new string of the whole text per concatenation
// (concatstrings copies the old text each time): hosts/mygo's linesIn made
// 212 of them a frame of the window binary. On the IR such a local is a
// runtime/v1 StrBuf (runtime/v1/strbuf.go): each concatenation appends its
// parts to one byte buffer the loop keeps, and a read makes a string only
// when the text changed since the last read. `s = e` shares e's bytes, as
// the plain local does.
//
// A local takes the form when every write of it is a concatenation onto
// itself (`s = s + p1 + … + pn`, `s += p`, with s in no part) or a plain
// assignment that does not read it (`s = e`), it is neither read nor
// written in a function literal, its type is string, and some
// concatenation of it is in a loop its let is outside of: there the buffer
// pays for itself. Any read is allowed; it is StrGet, which copies only
// after a change, so a read costs at most the allocation the concatenation
// before it made in Go. The only allocation Go did not make is the
// buffer's, once per run of the function that concatenates.
//
// The parts are evaluated in order, into temporaries when there are
// several, before the first is appended: a part that fails leaves the
// local as it was, as the concatenation does.
//
// The reference is strBufLowering "off": the local is a Go string and the
// concatenation Go's. TestStrBufAgrees runs its programs under both.
//
// Prior art: Java's javac lowers one concatenation expression to a
// StringBuilder, but not a loop of them; Rust's String::push_str and
// Swift's String append grow one buffer, as here, and Go's strings.Builder
// is the library form. JavaScript engines keep ropes. Gleam and Elixir
// build iodata lists and flatten once; ReScript compiles to JS's `+`.
var strBufLowering string

// SetStrBuf turns the built string on (the rule) or off (the reference),
// for a test of another package; restore undoes it.
func SetStrBuf(on bool) (restore func()) {
	saved := strBufLowering
	strBufLowering = "off"
	if on {
		strBufLowering = ""
	}
	return func() { strBufLowering = saved }
}

// strBufVar is a candidate local's facts while the body is walked.
type strBufVar struct {
	loops int             // the loops around its let
	fn    *syntax.FuncLit // the function literal its let is in, or nil
	bad   bool
	grown bool // a concatenation in a loop its let is outside of
}

// strBufVars are the locals of body that take the built string's form.
func strBufVars(info *check.Info, body syntax.Node) map[*check.Var]bool {
	if strBufLowering == "off" || body == nil {
		return nil
	}
	cands := map[*check.Var]*strBufVar{}
	varOf := func(x syntax.Expr) (*check.Var, *strBufVar) {
		id, ok := unparen(x).(*syntax.Ident)
		if !ok {
			return nil, nil
		}
		v, _ := info.Uses[id].(*check.Var)
		return v, cands[v]
	}
	var walk func(n syntax.Node, loops int, fn *syntax.FuncLit)
	walk = func(n syntax.Node, loops int, fn *syntax.FuncLit) {
		syntax.Inspect(n, func(n syntax.Node) bool {
			switch s := n.(type) {
			case *syntax.FuncLit:
				if s != fn {
					walk(s.Body, loops, s)
					return false
				}
			case *syntax.ForStmt:
				walk(s.Cond, loops+1, fn)
				walk(s.Body, loops+1, fn)
				return false
			case *syntax.RangeStmt:
				walk(s.X, loops, fn)
				walk(s.Key, loops+1, fn)
				walk(s.Value, loops+1, fn)
				walk(s.Body, loops+1, fn)
				return false
			case *syntax.DeclStmt:
				d := s.Decl
				if d.Mut.IsValid() && len(d.Specs) == 1 && d.Specs[0].Pattern == nil && len(d.Specs[0].Names) == 1 && len(d.Specs[0].Values) == 1 {
					if v, ok := info.Defs[d.Specs[0].Names[0]].(*check.Var); ok && v.Type == check.Typ[check.String] {
						cands[v] = &strBufVar{loops: loops, fn: fn}
					}
				}
			case *syntax.AssignStmt:
				for _, l := range s.Lhs {
					if _, c := varOf(l); c != nil && len(s.Lhs) != 1 {
						c.bad = true
					}
				}
				if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
					return true
				}
				v, c := varOf(s.Lhs[0])
				if c == nil {
					return true
				}
				if c.fn != fn {
					c.bad = true
				}
				var parts []syntax.Expr
				switch s.Tok {
				case syntax.ADD_ASSIGN:
					parts = []syntax.Expr{s.Rhs[0]}
				case syntax.ASSIGN:
					parts = catParts(info, s.Rhs[0], v)
					if parts == nil {
						// s = e, which must not read s.
						if namesVar(info, s.Rhs[0], v) {
							c.bad = true
						}
						walk(s.Rhs[0], loops, fn)
						return false
					}
				default:
					c.bad = true
					return true
				}
				for _, p := range parts {
					if namesVar(info, p, v) {
						c.bad = true
					}
					walk(p, loops, fn)
				}
				if loops > c.loops {
					c.grown = true
				}
				return false
			case *syntax.Ident:
				if _, c := varOf(s); c != nil && c.fn != fn {
					c.bad = true
				}
			}
			return true
		})
	}
	walk(body, 0, nil)
	var out map[*check.Var]bool
	for v, c := range cands {
		if c.bad || !c.grown {
			continue
		}
		if out == nil {
			out = map[*check.Var]bool{}
		}
		out[v] = true
	}
	return out
}

// strCall is the call of runtime/v1's name on the StrBuf local i, with
// args after it.
func (b *irBuilder) strCall(name string, i int, p irPos, args ...*irExpr) *irExpr {
	b.keepImport("var _ = " + b.l.rt() + "." + name)
	x := &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: i}}
	return &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: name, Args: append([]*irExpr{x}, args...)}
}

// strLet is the let of v, of the value x, as a StrBuf when v takes the
// built string's form; ok false otherwise.
func (b *irBuilder) strLet(v *check.Var, x syntax.Expr, p irPos) (out []*irStmt, ok bool) {
	if !b.strVars[v] {
		return nil, false
	}
	pre, e := b.value(x)
	i := b.temp(v.Name, b.spelling(b.l.rt()+".StrBuf"), p)
	if b.strBufs == nil {
		b.strBufs = map[*check.Var]int{}
	}
	b.strBufs[v] = i
	b.strSites++
	out = append(pre, &irStmt{K: "var", Pos: p, Local: i})
	return append(out, &irStmt{K: "expr", Pos: p, X: b.strCall("StrSet", i, p, e)}), true
}

// strAssign is the assignment s of v's StrBuf i: StrCat of each part of a
// concatenation, the parts evaluated first, or StrSet.
func (b *irBuilder) strAssign(s *syntax.AssignStmt, v *check.Var, i int) []*irStmt {
	p := b.pos(s, s.Pos())
	var parts []syntax.Expr
	switch s.Tok {
	case syntax.ADD_ASSIGN:
		parts = []syntax.Expr{s.Rhs[0]}
	case syntax.ASSIGN:
		parts = catParts(b.l.info, s.Rhs[0], v)
	default:
		irNo("built string assignment %s", s.Tok) // strBufVars admits none
	}
	if parts == nil {
		pre, e := b.value(s.Rhs[0])
		return append(pre, &irStmt{K: "expr", Pos: p, X: b.strCall("StrSet", i, p, e)})
	}
	var out []*irStmt
	vals := make([]*irExpr, len(parts))
	for k, part := range parts {
		pre, e := b.value(part)
		out = append(out, pre...)
		if len(parts) > 1 {
			// Each part is read before the first append, so a part that
			// fails or stops the program leaves the local as it was.
			t := b.temp("part", "string", p)
			out = append(out, &irStmt{K: "def", Pos: p, Local: t, X: e})
			e = &irExpr{K: "local", Pos: p, I: t}
		}
		vals[k] = e
	}
	for _, e := range vals {
		out = append(out, &irStmt{K: "expr", Pos: p, X: b.strCall("StrCat", i, p, e)})
	}
	return out
}

// catParts are the parts p1 … pn of x when x is s + p1 + … + pn, the
// concatenation of strings onto v's local s, or nil.
func catParts(info *check.Info, x syntax.Expr, v *check.Var) []syntax.Expr {
	var parts []syntax.Expr
	for {
		bin, ok := unparen(x).(*syntax.BinaryExpr)
		if !ok || bin.Op != syntax.ADD || info.Types[bin] != check.Typ[check.String] {
			break
		}
		parts = append(parts, bin.Y)
		x = bin.X
	}
	id, ok := unparen(x).(*syntax.Ident)
	if !ok || len(parts) == 0 || info.Uses[id] != v {
		return nil
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return parts
}

// namesVar reports whether x names v.
func namesVar(info *check.Info, x syntax.Node, v *check.Var) bool {
	found := false
	syntax.Inspect(x, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && info.Uses[id] == v {
			found = true
		}
		return !found
	})
	return found
}
