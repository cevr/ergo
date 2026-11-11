package check

import (
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// hostResourceParam includes interfaces such as io.Reader and pointer
// parameters: a call may convert a resource to an interface or lend an
// address. Neither shape establishes that Go returns before its last use.
func (c *checker) hostResourceParam(t Type) bool {
	if _, ok := t.(*Pointer); ok {
		return true // a Go pointer may retain even a non-resource borrow
	}
	return isHostNamed(t) && asInterface(t) != nil || c.mayHoldResource(t)
}

// hostClauses supplies the conservative clause even for an old or edited
// module binding. A binding cannot grant a narrower lifetime by writing a
// plain function type or omitting takes. Only reviewed tables can do that.
func (c *checker) hostClauses(target string, sig *Signature, at syntax.Node) {
	off := 0
	if d, ok := at.(*syntax.FuncDecl); ok && d.Recv != nil {
		off = 1
		if !v1.GoArgLent(target, 0) {
			sig.TakesSelf = true
		}
	} else if _, ok := at.(*syntax.Field); ok && !v1.GoArgLent(target, -1) {
		// An interface operation has an implicit receiver, not parameter
		// zero. Its Go body is unknown too.
		sig.TakesSelf = true
	}
	for i, p := range sig.Params {
		if s, ok := under0(p.Type).(*Signature); ok {
			if s.NonEscaping && v1.GoSyncCallbacks[target] == "" {
				c.errorf(at, CodeType, "%s declares a non-escaping Go callback without a reviewed clause; regenerate its binding", target)
			}
			continue
		}
		if c.hostResourceParam(p.Type) && !v1.GoArgLent(target, i+off) && !slices.Contains(sig.Takes, i) {
			sig.Takes = append(sig.Takes, i)
		}
	}
}
