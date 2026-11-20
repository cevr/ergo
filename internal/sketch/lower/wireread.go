package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// A format reads its own types (check.WireRead; types.md, Codecs 11), the
// read mirror of wirewrite.go. The decode function of a type X with a
// WireRead reads X by that function when the reader is an R, the read's
// direct reader, and by X's ordinary form otherwise:
//
//   - on a direct reader (wirereader.go) the choice is static: the decode
//     function made for R's direct reader is the call WireRead(&x, r),
//     and one made for another direct reader has the ordinary form;
//   - on the Reader interface (the reference), the function asks whether
//     r is the operations adapter of R first. The adapter exists when the
//     program converts an R into the Reader (the naming pass names it);
//     without one, no Reader can be an R, and the function has the
//     ordinary form alone.
//
// So both paths read the same value and fail the same failure for the
// same reader, which TestCellReaderAgrees' fast, reference and eval runs
// compare. Lane B's IR decoder builder takes the text body this writes
// (its form: own read, under codec reader).

// wireOwnRead writes X's own read into b and reports whether the body is
// complete (the direct path from R); otherwise the caller writes X's
// ordinary form after it.
func (l *lowerer) wireOwnRead(tc *typeCtx, b *strings.Builder, t check.Type, env, r, zero string, scope *namer) bool {
	hook, ok := check.WireReadOf(t)
	if !ok {
		return false
	}
	x, err := scope.alloc("x"), scope.alloc("err")
	call := func(reader string) string {
		return "var " + x + " " + tc.goType(t) + "\n" +
			"if " + err + " := " + l.wireCallFunc(hook.Func, env, tc.site, "&"+x, reader) + "; " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\n" +
			"return " + x + ", nil\n"
	}
	if tc.readVia > 0 {
		if !sameDecl(l.directReaders[tc.readVia-1].t, hook.Reader) {
			return false
		}
		b.WriteString(call(r))
		return true
	}
	key := l.typeKey(hook.Reader) + " => " + l.typeKey(check.PreludeType(check.WireReaderName))
	name, ok := l.pkgNames[pkgKey{"adapter", key}]
	if !ok {
		return false
	}
	a, is := scope.alloc("own"), scope.alloc("ok")
	b.WriteString("if " + a + ", " + is + " := " + r + ".(*" + name + "); " + is + " {\n" + call(a+".v") + "}\n")
	return false
}
