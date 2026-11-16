package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Context values (ORACLE O2; rows.md, Context values; runtime.md, G1).

// contextValue is a Context[R] value: the providers of R, built once
// where the value is made, as a with of the context builds them
// (Info.ContextValues). `with v { … }` (With.Installs) binds them for its
// block and builds nothing.
type contextValue struct {
	binds []capEnv // key and op of each capability of R; next unused
}

func (*contextValue) describe() string { return "a Context value" }

// makeContextValue builds the Context value of the context x converts
// (fact w).
func (in *interp) makeContextValue(fr *frame, x syntax.Expr, w *check.With) (Value, *abrupt) {
	for _, a := range w.Acquired {
		if a.Releases() {
			return nil, fr.unsupported(x, "a Context value whose provider is a resource yet")
		}
	}
	caps, _, ab := in.acquire(fr, x, w)
	if ab != nil {
		return nil, ab
	}
	if ab := in.build(fr, x, 1, int64(len(w.Discharged)), 0); ab != nil {
		return nil, ab
	}
	cv := &contextValue{}
	for _, cap := range w.Discharged {
		key := typeKey(fr.rt(cap))
		op, ok := caps.lookup(key)
		if !ok {
			ice("a Context value does not bind %s", cap)
		}
		cv.binds = append(cv.binds, capEnv{key: key, op: op})
	}
	return cv, nil
}

// ctxBinding is one context value in force: the package that declares
// its type, its key, its text (runtime/v1 context.go's pair, kept here
// independently).
type ctxBinding struct {
	pkg, key, text string
	next           *ctxBinding
}

// bindContextValue runs `with v { … }` for a v of the context-binding
// protocol (With.Value): it evaluates v once, calls its contextKey and
// contextText, and binds the text under the key, in the namespace of the
// package that declares v's type, for the block and every call it makes;
// the block's end, on every exit, restores the values before it. The
// values belong to the running task, not to a frame, so they are the
// interpreter's (one task runs at a time).
func (in *interp) bindContextValue(fr *frame, arg syntax.Expr, b *check.ContextBinding, inner func() (Value, *abrupt)) (Value, *abrupt) {
	if b.KeyInst != nil || b.TextInst != nil {
		return nil, fr.unsupported(arg, "a generic context binding yet")
	}
	v, ab := in.expr(fr, arg)
	if ab != nil {
		return nil, ab
	}
	key, ab := in.callResolved(fr, nil, b.Key, nil, []Value{v})
	if ab != nil {
		return nil, releaseOutcome(fr, arg, ab)
	}
	text, ab := in.callResolved(fr, nil, b.Text, nil, []Value{v})
	if ab != nil {
		return nil, releaseOutcome(fr, arg, ab)
	}
	n, ok := fr.rt(b.Type).t.(*check.Named)
	if !ok || n.Obj.Pkg == nil {
		ice("a context binding of %s, which no package declares", b.Type)
	}
	saved := in.ctxv
	in.ctxv = &ctxBinding{pkg: n.Obj.Pkg.Path, key: key.(string), text: text.(string), next: saved}
	defer func() { in.ctxv = saved }()
	return inner()
}

// contextLookup is the innermost text bound under pkg and key.
func (in *interp) contextLookup(pkg, key string) (string, bool) {
	for c := in.ctxv; c != nil; c = c.next {
		if c.pkg == pkg && c.key == key {
			return c.text, true
		}
	}
	return "", false
}
