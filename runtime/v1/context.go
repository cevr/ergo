package v1

import "ergo.local/prototype/runtime/rt"

// Context values (docs/spec/runtime.md, G1). `with v { … }`, where v's type
// satisfies the context-binding protocol, binds v's text under v's key for
// the block and for every task started inside it. rt keeps a task's values
// as an immutable list that a child shares at its start (rt.Task.WithValue),
// so a task sees the values in force when it started and no later change,
// and nothing mutable is shared across goroutines. This file is the one v1
// adapter over that storage: OpenContext and Context.End bound a region,
// and ContextBound and ContextValue read it.
//
// A region is opened on the scope of the code at the with, its frame or the
// innermost owning with, as an owning with's scope is (OpenWith). The block's
// normal end ends it (Context.End); every other exit, a typed failure that
// returns, a defect or a stop, ends it when the scope closes, innermost
// region first (Scope.close). So a binding is restored on every exit.

// region is a lexical region open on a scope: an owning with's scope or a
// context region. close ends it on an exit that leaves its block early.
type region interface {
	close(e error) error
}

// contextKey keys a context value in the task's list, so no other user of
// rt's values can collide with a binding.
type contextKey string

// Context is an open context region of one scope.
type Context struct {
	s     *Scope
	saved rt.Context
}

// OpenContext binds text under key for the code at s and every task it
// starts until the region ends. s is the scope of the code at the with.
func (s *Scope) OpenContext(key, text string) *Context {
	s = s.current()
	if s.ended {
		panic("with: the frame that would hold this context value has returned")
	}
	c := &Context{s: s, saved: s.s.Task().WithValue(contextKey(key), text)}
	s.withs = append(s.withs, c)
	return c
}

// End ends the context region c at the normal end of its block: the values
// in force before it are restored. Tasks already started keep theirs.
func (c *Context) End() {
	s := c.s
	n := len(s.withs)
	if n == 0 || s.withs[n-1] != region(c) {
		panic("with: a context region ended out of its nesting")
	}
	s.withs = s.withs[:n-1]
	c.saved.End()
}

// close ends the region on an early exit of its scope.
func (c *Context) close(e error) error {
	c.saved.End()
	return e
}

// ContextBound reports whether a context value is bound to key for the
// task t.
func ContextBound(t *rt.Task, key string) bool {
	return t.Value(contextKey(key)) != nil
}

// ContextValue is the text bound to key for the task t, the innermost
// binding, or "" when none is.
func ContextValue(t *rt.Task, key string) string {
	v, _ := t.Value(contextKey(key)).(string)
	return v
}
