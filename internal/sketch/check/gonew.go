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

// goNewShares is the path of a field of the Go struct t, through its
// struct fields and arrays, whose type is a slice or a Map, or "". A
// go.Ref field is Go's memory already, and an Option of one is too.
func goNewShares(t Type, seen map[Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	s := asStruct(t)
	if s == nil {
		return ""
	}
	for _, f := range s.Fields {
		ft := f.Type
		for {
			a, ok := under(ft).(*Array)
			if !ok {
				break
			}
			ft = a.Elem
		}
		switch under(ft).(type) {
		case *Slice, *Map:
			return f.Name
		}
		if n, ok := ft.(*Named); ok && refElem(n) != nil {
			continue
		}
		if p := goNewShares(ft, seen); p != "" {
			return f.Name + "." + p
		}
	}
	return ""
}

// goZeroCall checks go.Zero[T](): Go's zero T, a value.
func (c *checker) goZeroCall(x *syntax.CallExpr, targ Type) operand {
	if x.Trailing != nil || len(x.Args) != 0 {
		c.errorf(x, CodeType, "go.Zero[T]() takes no argument and no block: its value is Go's zero T")
		c.silentArgs(x)
		return bad(x)
	}
	if targ == nil {
		c.errorf(x.Fun, CodeType, "go.Zero needs the Go type whose zero it is: go.Zero[T](), T a Go type of import go (docs/spec/interop.md)")
		return bad(x)
	}
	if isInvalid(targ) {
		return bad(x)
	}
	if why := c.goNewTarget(targ); why != "" {
		c.errorf(x.Fun, CodeType, "go.Zero[%s]: %s", targ, why)
		return bad(x)
	}
	return operand{mode: modeValue, typ: targ}
}

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

// go.New(v) is a go.Ref[T] to a new Go T that holds a copy of v (GBX5b):
// the way Ergo source sets a Go struct's fields at construction, as hand
// Go writes &syscall.SysProcAttr{Setpgid: true}. Go's fields stay read-only
// after it (q34). T reaches no slice or Map: a Go write through the
// go.Ref would reach one an Ergo value shares (types.md, Bindings 2).
//
// go.Zero[T]() is Go's zero T as a value: the explicit base a literal of a
// Go struct spreads from, syscall.SysProcAttr{..go.Zero[syscall.SysProcAttr](),
// Setpgid: true}, since a literal names every field (owner q44). It has no
// identity, so it charges nothing.

// goNewCall checks go.New[T]() and go.New(v); targ is the written type
// argument (nil when none is written).
func (c *checker) goNewCall(x *syntax.CallExpr, targ Type) operand {
	if x.Trailing != nil {
		c.errorf(x.Trailing, CodeType, "go.New takes no block")
		c.silentArgs(x)
		return bad(x)
	}
	if len(x.Args) > 1 {
		c.errorf(x, CodeType, "go.New takes no argument (Go's zero T) or one, the Go value it holds; not %d", len(x.Args))
		c.silentArgs(x)
		return bad(x)
	}
	if len(x.Args) == 1 {
		v := c.valueExpr(x.Args[0], targ)
		if v.invalid() {
			return bad(x)
		}
		if targ == nil {
			targ = v.typ
		} else if !c.assign(&v, targ, "argument") {
			return bad(x)
		}
		if why := c.goNewTarget(targ); why != "" {
			c.errorf(x.Args[0], CodeType, "go.New(v) of %s: %s", targ, why)
			return bad(x)
		}
		if p := goNewShares(targ, map[Type]bool{}); p != "" {
			c.errorf(x.Args[0], CodeType, "go.New(v) of %s: its field %s holds a slice or Map that an Ergo value may share, and Go could write it through the go.Ref; take the go.Ref from a Go function", targ, p)
			return bad(x)
		}
		c.charge(x, goForeign()) // a new Go value has identity, as go.New[T]()'s has
		return operand{mode: modeValue, typ: c.instantiate(universeOnce.ref, []Type{targ})}
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
