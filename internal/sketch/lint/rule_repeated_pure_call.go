package lint

import (
	"go/constant"
	"go/token"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// repeatedPureCall is the defensive rule of the strict preset (catalog
// CR9, data decoded twice): in one frame, a call of a pure value function
// (check.PureValueFunc: no capability, no task, no unknown code, no
// pointer, a plain value as its result) with the same arguments as an
// earlier call that runs whenever it does. The arguments are constants or
// names that hold one plain value for the frame (a parameter or a plain
// let of a basic type: a `let mut` may change between the calls, and a
// slice, a map or a record holding one may change through another name). An earlier call runs whenever
// the later one does when it is in the same block or one that encloses
// it, before it: not in another branch, arm, loop body, the right operand
// of && or ||, a function literal or a call's trailing block.
var repeatedPureCall = &Rule{
	Name:    "repeated-pure-call",
	Code:    "L127",
	Doc:     "A pure call with the same arguments as an earlier one in the same frame computes the same value again.",
	Instead: "bind the first call's value with let and read the name",
	Default: Warning,
	Check:   checkRepeatedPureCall,
}

// pureCall is a call the rule has seen: its function, its arguments and
// the blocks around it, outermost first.
type pureCall struct {
	fn     *check.Func
	args   []syntax.Expr
	blocks []syntax.Node
}

func checkRepeatedPureCall(p *Pass) {
	for _, d := range p.File.Decls {
		switch d := d.(type) {
		case *syntax.FuncDecl:
			if d.Body != nil {
				repeatedInFrame(p, d.Body)
			}
		case *syntax.TestDecl:
			if d.Body != nil {
				repeatedInFrame(p, d.Body)
			}
		case *syntax.LawDecl:
			if d.Body != nil {
				repeatedInFrame(p, d.Body)
			}
		case *syntax.TypeDecl:
			walkNode(d, func(n syntax.Node) bool {
				if l, ok := n.(*syntax.LawDecl); ok && l.Body != nil {
					repeatedInFrame(p, l.Body)
				}
				return true
			})
		}
	}
	for _, fl := range p.fileIndex().funcLits {
		if fl.Body != nil {
			repeatedInFrame(p, fl.Body)
		}
	}
}

// repeatedInFrame reports the repeated calls of one frame.
func repeatedInFrame(p *Pass, body syntax.Node) {
	var seen []pureCall
	var walk func(n syntax.Node, blocks []syntax.Node)
	block := func(n syntax.Node, blocks []syntax.Node) {
		if n != nil && !syntax.IsNil(n) {
			walk(n, append(blocks[:len(blocks):len(blocks)], n))
		}
	}
	walk = func(n syntax.Node, blocks []syntax.Node) {
		walkNode(n, func(n syntax.Node) bool {
			switch x := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CatchArm:
				if len(blocks) == 0 || blocks[len(blocks)-1] != n {
					block(n, blocks)
					return false
				}
			case *syntax.IfStmt:
				// a loop's body and a branch are blocks of their own; an
				// else if is too, its condition included
				walk(x.Cond, blocks)
				block(x.Body, blocks)
				if x.Else != nil {
					block(x.Else, blocks)
				}
				return false
			case *syntax.BinaryExpr:
				if x.Op == syntax.LAND || x.Op == syntax.LOR {
					walk(x.X, blocks)
					block(x.Y, blocks)
					return false
				}
			case *syntax.CallExpr:
				if x.Trailing != nil {
					if c := p.Info.Calls[x]; c == nil || c.Kind != check.CallWith {
						walk(x.Fun, blocks)
						for _, a := range x.Args {
							walk(a, blocks)
						}
						return false
					}
				}
				if c, ok := pureCallOf(p.Info, x, blocks); ok {
					for _, s := range seen {
						if s.fn == c.fn && sameArgs(p.Info, s.args, c.args) && encloses(s.blocks, c.blocks) {
							p.Report(x.Pos(), x.End(), "this call of "+c.fn.Name+" repeats an earlier call with the same arguments in this frame")
							break
						}
					}
					seen = append(seen, c)
				}
			}
			return true
		})
	}
	walk(body, []syntax.Node{body})
}

// pureCallOf is x as a call of a pure value function whose arguments
// (a dot call's receiver first) are constants or names that hold one
// value for the frame.
func pureCallOf(info *check.Info, x *syntax.CallExpr, blocks []syntax.Node) (pureCall, bool) {
	c := info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || !check.PureValueFunc(c.Func) {
		return pureCall{}, false
	}
	args := x.Args
	if c.Recv != nil {
		args = append([]syntax.Expr{c.Recv}, args...)
	}
	if len(args) == 0 {
		return pureCall{}, false
	}
	for _, a := range args {
		a = unparen(a)
		if info.Values[a] != nil {
			continue
		}
		id, ok := a.(*syntax.Ident)
		if !ok {
			return pureCall{}, false
		}
		v, ok := info.Uses[id].(*check.Var)
		if !ok || v.Mutable() || !check.PlainValue(v.Type) {
			return pureCall{}, false
		}
	}
	return pureCall{fn: c.Func, args: args, blocks: blocks}, true
}

// sameArgs reports arguments that are the same constants and the same
// names, in order.
func sameArgs(info *check.Info, a, b []syntax.Expr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := unparen(a[i]), unparen(b[i])
		vx, vy := info.Values[x], info.Values[y]
		switch {
		case vx != nil && vy != nil:
			if vx.Kind() != vy.Kind() || !constant.Compare(vx, token.EQL, vy) {
				return false
			}
		case vx == nil && vy == nil:
			ix, _ := x.(*syntax.Ident)
			iy, _ := y.(*syntax.Ident)
			if ix == nil || iy == nil || info.Uses[ix] != info.Uses[iy] {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// encloses reports whether the earlier call's blocks are the later one's
// or enclose them.
func encloses(outer, inner []syntax.Node) bool {
	if len(outer) > len(inner) {
		return false
	}
	for i := range outer {
		if outer[i] != inner[i] {
			return false
		}
	}
	return true
}
