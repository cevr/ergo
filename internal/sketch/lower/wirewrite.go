package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// A format writes its own types (check.WireWrite; types.md, Codecs 9).
// The codec function of a type X with a WireWrite writes X by that
// function when the writer is a cell of its provider P, and by X's
// ordinary form otherwise:
//
//   - on a direct writer (wiredirect.go) the choice is static: the codec
//     function made for P's cell is the call WireWrite(x, w), and one
//     made for another provider has the ordinary form;
//   - on the Writer interface (the reference), the function asks whether
//     w is the adapter of P's cell first. The adapter exists when the
//     program converts a P into its operation type (the naming pass
//     names it); without one, no Writer can be a cell of P, and the
//     function has the ordinary form alone.
//
// So both paths write the same events for the same writer, which
// TestCellWriterAgrees' fast, reference and Writer builds compare.

// wireOwnWrite writes X's own write on the path em into b and reports
// whether the body is complete (the direct path to P's cell); otherwise
// the caller writes X's ordinary form after it.
func (l *lowerer) wireOwnWrite(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, w, v string, scope *namer) bool {
	hook, ok := check.WireWriteOf(t)
	if !ok {
		return false
	}
	if em.via >= 0 {
		cw, cell := l.cellOf(em)
		if !cell || cw.prov.Obj != hook.Provider.Obj {
			return false
		}
		// w is *P (cellFuncs).
		b.WriteString(l.wireCallFunc(hook.Func, env, tc.site, v, w) + "\n")
		return true
	}
	conv, ok := check.CellProvider(hook.Op)
	if !ok {
		return false
	}
	key, _, ok := l.adapterName(conv)
	if !ok {
		return false
	}
	name, ok := l.pkgNames[pkgKey{"adapter", key}]
	if !ok {
		return false
	}
	a, is := scope.alloc("own"), scope.alloc("ok")
	cell := "&" + a + ".v"
	if _, ptr := conv.From.(*check.Pointer); ptr {
		cell = a + ".v"
	}
	b.WriteString("if " + a + ", " + is + " := " + w + ".(*" + name + "); " + is + " {\n" + l.wireCallFunc(hook.Func, env, tc.site, v, cell) + "\nreturn\n}\n")
	return false
}
