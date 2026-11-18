package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// go.New[T]() is Go's new(T): a fresh zero T of a Go type, held by its
// address as a go.Ref[T] (GBX1; docs/spec/interop.md, Go values by
// address). It is how Ergo source holds a Go value whose methods have
// pointer receivers and which Go forbids to copy once used: a
// sync.Mutex, a sync.WaitGroup, an atomic.Int64. Copies of the go.Ref
// share the one Go value, as Go's pointers do.
//
// T is a Go type of import go, not an interface (Go has no value of one)
// and not a resource: a zero file or connection is no open one, and its
// release would run on nothing. The call charges go.Foreign: the value
// it makes has identity, which no pure frame may create (a pure call may
// be evaluated once for two uses).

// goNewTarget is why T is no type of go.New, or "".
func (c *checker) goNewTarget(t Type) string {
	n, ok := t.(*Named)
	if !ok || !isHostNamed(n) || refElem(n) != nil {
		return fmt.Sprintf("%s is not a Go type of import go", t)
	}
	if asInterface(n) != nil {
		return fmt.Sprintf("%s is a Go interface, which has no value of its own", n)
	}
	if c.holdsResource(n) || c.hostClose(n) != nil {
		return fmt.Sprintf("%s is a resource, and a zero one is not open; take it from the Go function that opens it", n)
	}
	return ""
}

// goNewCall checks go.New[T](); targ is the written type argument (nil
// when none is written).
func (c *checker) goNewCall(x *syntax.CallExpr, targ Type) operand {
	if x.Trailing != nil {
		c.errorf(x.Trailing, CodeType, "go.New takes no block")
		c.silentArgs(x)
		return bad(x)
	}
	if len(x.Args) != 0 {
		c.errorf(x, CodeType, "go.New[T]() takes no argument, not %d: its value is Go's zero T", len(x.Args))
		c.silentArgs(x)
		return bad(x)
	}
	if targ == nil {
		c.errorf(x.Fun, CodeType, "go.New needs the Go type it makes: go.New[T](), T a Go type of import go (docs/spec/interop.md)")
		return bad(x)
	}
	if isInvalid(targ) {
		return bad(x)
	}
	if why := c.goNewTarget(targ); why != "" {
		c.errorf(x.Fun, CodeType, "go.New[%s]: %s", targ, why)
		return bad(x)
	}
	c.charge(x, goForeign())
	return operand{mode: modeValue, typ: c.instantiate(universeOnce.ref, []Type{targ})}
}
