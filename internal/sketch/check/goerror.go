package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// go.Is and go.As (IG5; docs/spec/interop.md, Go errors). A go.Error keeps
// Go's native error, and these two read its chain as Go's errors.Is and
// errors.As do:
//
//   - go.Is(e go.Error, target go.Error) bool;
//   - go.As[T](e go.Error) Option[T], T a Go type that implements error
//     (a binding's type whose Go method set has Error() string, by value
//     or through a go.Ref) or a Go interface: errors.As takes only these.
//
// Both are compiler-known functions of the go qualifier, as go.Error is:
// As needs its type argument checked as a host type, which no Ergo
// signature can state. Both are pure, with no row; neither is a value.

// goErrorBuiltin reports whether b is go.Is or go.As.
func goErrorBuiltin(b *Builtin) bool { return b.Name == "go.Is" || b.Name == "go.As" }

// goErrorType is the predeclared go.Error.
func goErrorType() Type {
	universe()
	return universeOnce.goPkg.Scope.names["Error"].(*TypeName).Type
}

// goErrorCall checks a call of go.Is or go.As; targ is As's written type
// argument (nil when none is written).
func (c *checker) goErrorCall(x *syntax.CallExpr, b *Builtin, targ Type) operand {
	if x.Trailing != nil {
		c.errorf(x.Trailing, CodeType, "%s takes no block", b.Name)
		c.silentArgs(x)
		return bad(x)
	}
	want := 2
	if b.Name == "go.As" {
		want = 1
	}
	if len(x.Args) != want {
		c.errorf(x, CodeType, "%s takes %d go.Error argument%s, not %d", b.Name, want, plural(want), len(x.Args))
		c.silentArgs(x)
		return bad(x)
	}
	ok := true
	for _, a := range x.Args {
		op := c.valueExpr(a, goErrorType())
		if op.invalid() || !c.assign(&op, goErrorType(), "argument") {
			ok = false
		}
	}
	if b.Name == "go.Is" {
		if !ok {
			return bad(x)
		}
		return operand{mode: modeValue, typ: Typ[Bool]}
	}
	if targ == nil {
		c.errorf(x.Fun, CodeType, "go.As needs the Go type to find in the chain: go.As[T](e), T a Go error type or a Go interface (docs/spec/interop.md)")
		return bad(x)
	}
	if isInvalid(targ) || !ok {
		return bad(x)
	}
	if why := c.goAsTarget(targ); why != "" {
		c.errorf(x.Fun, CodeType, "go.As[%s]: %s; T is a Go type whose method set has Error() string (a value or a go.Ref), or a Go interface, as errors.As takes (docs/spec/interop.md)", targ, why)
		return bad(x)
	}
	return operand{mode: modeValue, typ: c.instantiate(preludeType("Option"), []Type{targ})}
}

// goAsTarget is why T is no target of go.As, or "".
func (c *checker) goAsTarget(t Type) string {
	handle := false
	if e := refElem(t); e != nil {
		t, handle = e, true
	}
	n, ok := t.(*Named)
	if !ok || !isHostNamed(n) {
		return fmt.Sprintf("%s is not a Go type of import go", t)
	}
	if !handle && asInterface(n) != nil {
		return ""
	}
	sig, fn := c.methodOf(n, "Error")
	if sig == nil || len(sig.Params) != 0 || len(sig.Results) != 1 || !isString(sig.Results[0]) || sig.Fallible {
		return fmt.Sprintf("%s has no method Error() string", n)
	}
	if fn != nil && hostOf(n.Obj.Pkg).recvPtr[fn] && !handle {
		return fmt.Sprintf("%s implements error through a pointer receiver; write go.Ref[%s]", n, n)
	}
	return ""
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
