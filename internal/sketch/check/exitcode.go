package check

import (
	"go/constant"
	"go/token"

	"ergo.local/prototype/internal/sketch/syntax"
)

// exitCodeRange reports the S108 of a constant Code of a std os.Exit
// literal outside 1..125 (G-ex-c-17): the entry would write an error line
// and exit 1 (runtime.md, Exit causes 2), so the checker says so where the
// value is written. A Code computed at run time is still checked by the
// entry.
func (c *checker) exitCodeRange(t Type, field string, op operand, at syntax.Node) {
	n, ok := t.(*Named)
	if !ok || field != "Code" || n.Obj == nil || n.Obj.Name != "Exit" || n.Obj.Pkg == nil || n.Obj.Pkg.Path != "os" {
		return
	}
	if op.mode != modeConst || op.val == nil {
		return
	}
	v := constant.ToInt(op.val)
	if v.Kind() != constant.Int {
		return
	}
	if constant.Compare(v, token.LSS, constant.MakeInt64(1)) || constant.Compare(v, token.GTR, constant.MakeInt64(125)) {
		c.errorf(at, CodeType, "os.Exit code %s is outside 1..125: 0 claims success and 126 and up are the shell's (runtime.md, Exit causes 2)", v.ExactString())
	}
}
