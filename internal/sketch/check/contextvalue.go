package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The context-binding protocol (runtime.md, G1). `with v { … }` binds a
// context value when v's type has two functions on it,
//
//	func contextKey(v T) string
//	func contextText(v T) string
//
// each with empty error and capability rows: the type satisfies the
// protocol by the rule every operation type uses (implements; types.md,
// Methods). Admission reads the operand's type alone, never the package or
// the name of the function that made the value, so a user type that
// satisfies the protocol binds as std's trace.Span and log.Annotate do, and
// a same-shaped type whose functions need a capability, can fail or return
// another type does not. With.Value records the binding for the lowering.
// The names are unexported, as a Go method that only its package's
// protocol calls: the protocol adds no exported declaration to trace or
// log (approval item 4 lists four), and a user package declares its own.
// The binding type's declaring package namespaces its keys; lowering reads
// that identity from ContextBinding.Type, not from the operand's spelling.
//
// Prior art: Java's ScopedValue binds a typed key for a dynamic extent;
// Effect's References are typed keys with defaults; Go's context.WithValue
// threads the binding by hand. Ergo's v1 binding is a string pair; typed
// payloads are a follow-up.

// contextArg checks a with operand that is not a context, as an argument is
// checked: in the enclosing frame, before the with's scope opens, so its
// failures are handled there. It returns the operand's context binding, or
// nil and false when the with is unknown: an invalid operand is reported
// already, and one whose type does not satisfy the protocol is reported
// here, with outside's message when outside is set. An unknown with
// discharges everything, so nothing cascades (D3).
func (c *checker) contextArg(arg syntax.Expr, outside string) (*ContextBinding, bool) {
	return c.contextArgOf(arg, c.valueExpr(arg, nil), outside)
}

// contextArgOf is contextArg of the operand op, arg already checked.
func (c *checker) contextArgOf(arg syntax.Expr, op operand, outside string) (*ContextBinding, bool) {
	if op.invalid() || op.typ == nil || isInvalid(op.typ) {
		return nil, false
	}
	cb, why := c.contextBinding(op.typ)
	if cb != nil {
		return cb, true
	}
	if outside != "" {
		c.outside(arg, outside)
		return nil, false
	}
	c.errorf(arg, CodeType, "with takes a context or a context value, and %s is neither: %s (runtime.md, G1: a context value's type has the functions contextKey(v) string and contextText(v) string, with no needs and no failures)", op.typ, why)
	return nil, false
}

// contextBinding is t's context binding, or nil and the reason t does not
// satisfy the protocol. Only a named type's own functions bind: an
// operation type's operations run unknown code and have no function to
// call, and a borrow *T is not a value the binding can keep.
func (c *checker) contextBinding(t Type) (*ContextBinding, string) {
	n, ok := t.(*Named)
	if !ok {
		return nil, fmt.Sprintf("%s is not a named type", t)
	}
	if why := c.implements(n, contextProtocol()); why != "" {
		return nil, why
	}
	_, key := c.methodOf(n, "contextKey")
	_, text := c.methodOf(n, "contextText")
	if key == nil || text == nil {
		return nil, fmt.Sprintf("%s's contextKey and contextText are operations, not functions on the type", n)
	}
	cb := &ContextBinding{Type: t, Key: key, Text: text}
	// A generic protocol function is instantiated at the operand's type
	// arguments, as a dot call's receiver binds them (D-D8), so the
	// lowering calls the instance, with its witnesses, like any call.
	var why string
	if cb.KeyInst, why = c.witnessInstance(key, receiverTypeArgs(n, key)); why != "" {
		return nil, why
	}
	if cb.TextInst, why = c.witnessInstance(text, receiverTypeArgs(n, text)); why != "" {
		return nil, why
	}
	return cb, ""
}

// contextProtocol is the protocol as an interface: contextKey() string and
// contextText() string, with empty rows. It is made per use, so no checker
// shares a row with another.
func contextProtocol() *Interface {
	sig := func() *Signature {
		return &Signature{Results: []Type{Typ[String]}, Errors: newRow(), Uses: newRow()}
	}
	return &Interface{Methods: []*Method{
		{Name: "contextKey", Sig: sig()},
		{Name: "contextText", Sig: sig()},
	}}
}
