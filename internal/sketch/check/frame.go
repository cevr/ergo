package check

import "ergo.local/prototype/internal/sketch/syntax"

// inspectFrame visits the nodes evaluated in root's frame. Making a
// function literal or a trailing callback does not run its body. A call's
// callee and arguments do run here, including when it has a callback. A
// with body also runs here. withs is the path of with bodies entered since
// root, outermost first; a with's operands are outside its own body.
// A WireLit's provider expressions run here too, before its with's body:
// lexical returns and effects still belong to the enclosing source frame,
// even when the lowering uses a native callback to mask acquisition.
// Returning false from visit prunes that node's children.
func inspectFrame(info *Info, root syntax.Node, visit func(syntax.Node, []*syntax.CallExpr) bool) {
	var walk func(syntax.Node, []*syntax.CallExpr)
	walk = func(root syntax.Node, withs []*syntax.CallExpr) {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if !visit(n, withs) {
				return false
			}
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.CallExpr:
				if n.Trailing != nil {
					walk(n.Fun, withs)
					for _, a := range n.Args {
						walk(a, withs)
					}
					if call := info.Calls[n]; call != nil && call.Kind == CallWith {
						walk(n.Trailing, append(withs, n))
					}
					return false
				}
			}
			return true
		})
	}
	walk(root, nil)
}
