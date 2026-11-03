package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// BinderMeaning checks src as one package and reports what typing and the
// reference walk make of the identifier at offset off (fuzz-17d6). Typing
// says "binds" when Info.Defs records it as a local, "names" when Info.Uses
// records the package or prelude object it names (a type, a capability, a
// constant, or a function a pattern refuses), and "none", "uses a local" or
// the defined object's type otherwise. The walk says "names" when it took the
// identifier in a type or context position, "binds" when it visited it in
// any other way, and "unvisited" when no walk reached it. found is false
// when src does not parse or no identifier starts at off.
func BinderMeaning(src string, off int) (info, walk string, found bool) {
	file, err := syntax.Parse(src)
	if err != nil {
		return "", "", false
	}
	var id *syntax.Ident
	syntax.Inspect(file, func(n syntax.Node) bool {
		if x, ok := n.(*syntax.Ident); ok && int(x.Pos()) == off && id == nil {
			id = x
		}
		return id == nil
	})
	if id == nil {
		return "", "", false
	}
	c := newChecker("main", Std, prelude().scope)
	var walks []*refWalker
	refTrace = func(_ syntax.Node, w *refWalker) {
		if w.c == c {
			walks = append(walks, w)
		}
	}
	defer func() { refTrace = nil }()
	c.run([]*syntax.File{file})
	info = "none"
	if d, ok := c.pkg.Info.Defs[id]; ok {
		if v, isVar := d.(*Var); isVar && !v.pkg {
			info = "binds"
		} else {
			info = fmt.Sprintf("defines %T", d)
		}
	} else if u, ok := c.pkg.Info.Uses[id]; ok {
		if v, isVar := u.(*Var); isVar && !v.pkg {
			info = "uses a local"
		} else {
			info = "names"
		}
	}
	walk = "unvisited"
	for _, w := range walks {
		switch {
		case !w.visited[id]:
		case w.binders[id]:
			walk = "binds"
		case w.nameIdents[id]:
			walk = "names"
		default:
			walk = "value"
		}
	}
	return info, walk, true
}
