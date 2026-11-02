package rt

import (
	"fmt"
	"strings"
)

type interrupted struct{}

func (interrupted) Error() string { return "interrupted" }

// Interrupted is the exit of a task that observed a stop. It is not a typed
// failure: no error row names it, and every catch passes it on. Check panics
// with it; a body with an error row returns it.
var Interrupted error = interrupted{}

// Expired is timeout.Expired in source rows: the typed failure After adds when
// its deadline passes first.
type Expired struct{}

func (Expired) Error() string { return "timeout.Expired" }

// Kind classifies one reason of a cause.
type Kind uint8

const (
	// KindFailure is a typed failure from an error row.
	KindFailure Kind = iota + 1
	// KindDefect is a panic: a failure outside every row.
	KindDefect
	// KindInterruption is an observed stop.
	KindInterruption
)

// Frame is one task in a defect's chain (G2).
type Frame struct {
	Task uint64
	Span string
}

// Reason is one reason a task did not succeed.
type Reason struct {
	Kind Kind
	// Err is the typed failure, for KindFailure.
	Err error
	// Value is the panic value, for KindDefect.
	Value any
	// Chain is the failing task and its ancestors, failing task first, for
	// KindDefect.
	Chain []Frame
	// ChainCut marks a chain cut at its bound: the last frame's task still
	// had a parent when the defect copied it (G2, rule 5).
	ChainCut bool
	// InRelease marks a defect raised by a release, not by the body.
	InRelease bool
}

// Cause is the exit accumulator and the exit cause it builds: one or more
// typed failures, defects and interruptions, in the order they happened. The
// zero Cause is empty and ready to use. A Cause keeps at most one interruption
// reason.
type Cause struct {
	Reasons []Reason
}

// AddFailure records a typed failure.
func (c *Cause) AddFailure(err error) {
	c.Reasons = append(c.Reasons, Reason{Kind: KindFailure, Err: err})
}

// AddDefect records a panic value raised in task t.
func (c *Cause) AddDefect(t *Task, value any) { c.defect(t, value, false, 0) }

// AddInterruption records an interruption, once.
func (c *Cause) AddInterruption() {
	for _, r := range c.Reasons {
		if r.Kind == KindInterruption {
			return
		}
	}
	c.Reasons = append(c.Reasons, Reason{Kind: KindInterruption})
}

// Add records an error in canonical form: nil adds nothing, a non-nil *Cause
// adds its reasons, Interrupted adds an interruption and any other error,
// including a typed-nil *Cause, is a typed failure.
func (c *Cause) Add(err error) {
	switch e := err.(type) {
	case nil:
	case *Cause:
		if e == nil {
			c.AddFailure(err)
			return
		}
		for _, r := range e.Reasons {
			if r.Kind == KindInterruption {
				c.AddInterruption()
			} else {
				c.Reasons = append(c.Reasons, r)
			}
		}
	default:
		if err == Interrupted {
			c.AddInterruption()
		} else {
			c.AddFailure(err)
		}
	}
}

// transport carries an exit through code with no error row: Scope.End(nil) and
// Handle.Close panic with it, and a recovery unpacks only this type. Every
// other panic value, a *Cause included, is a defect recorded as it is.
type transport struct{ err error }

func (t transport) Error() string {
	if t.err == nil {
		return "rt: empty exit transport"
	}
	return t.err.Error()
}

// recovered records a recovered panic raised in task t. A transport with an
// exit, or the Interrupted sentinel that Check panics with, carries an exit;
// any other value is a defect. It never dereferences the payload, so a
// recovery cannot itself panic and skip cleanup.
//
// On a traced tree it first closes the capability calls the panic unwound:
// those opened since the recovering region began, when floor calls were open
// (traceDepth; 0 at a task boundary).
func (c *Cause) recovered(t *Task, p any, inRelease bool, floor int) {
	var left uint64
	if t.trace != nil {
		left = t.traceUnwind(p, floor)
	}
	switch e := p.(type) {
	case transport:
		if e.err != nil {
			c.Add(e.err)
			return
		}
	case error:
		if e == Interrupted {
			c.AddInterruption()
			return
		}
	}
	c.defect(t, p, inRelease, left)
}

// defect records p as a defect of t. left is the call-exit of the innermost
// capability call the panic unwound, 0 for none.
func (c *Cause) defect(t *Task, p any, inRelease bool, left uint64) {
	chain, cut := t.Chain()
	c.Reasons = append(c.Reasons, Reason{Kind: KindDefect, Value: p, Chain: chain, ChainCut: cut, InRelease: inRelease})
	if t.trace != nil {
		t.traceDefect(p, inRelease, left, &chain[0])
	}
}

// Err returns the accumulated exit in canonical form: nil when empty, a lone
// typed failure as itself, Interrupted for a lone interruption, and a copy of
// the Cause otherwise.
func (c *Cause) Err() error {
	switch len(c.Reasons) {
	case 0:
		return nil
	case 1:
		switch r := c.Reasons[0]; r.Kind {
		case KindFailure:
			return r.Err
		case KindInterruption:
			return Interrupted
		}
	}
	return &Cause{Reasons: append([]Reason(nil), c.Reasons...)}
}

// Error prints each reason on its own line; a defect is followed by its chain
// of tasks and spans, failing task first (G2).
func (c *Cause) Error() string {
	if c == nil {
		return "<nil *rt.Cause>"
	}
	var b strings.Builder
	for i, r := range c.Reasons {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch r.Kind {
		case KindFailure:
			fmt.Fprintf(&b, "failure: %v", r.Err)
		case KindDefect:
			if r.InRelease {
				fmt.Fprintf(&b, "defect in release: %v", r.Value)
			} else {
				fmt.Fprintf(&b, "defect: %v", r.Value)
			}
			for _, f := range r.Chain {
				fmt.Fprintf(&b, "\n  task #%d %s", f.Task, f.Span)
			}
			if r.ChainCut {
				fmt.Fprintf(&b, "\n  %s", ChainCutLine)
			}
		case KindInterruption:
			b.WriteString("interrupted")
		}
	}
	return b.String()
}

// Unwrap exposes the typed failures and the interruption to errors.Is and
// errors.As, for Go interop. A catch dispatches through Failure instead, which
// matches only a lone typed failure.
func (c *Cause) Unwrap() []error {
	if c == nil {
		return nil
	}
	var errs []error
	for _, r := range c.Reasons {
		switch r.Kind {
		case KindFailure:
			errs = append(errs, r.Err)
		case KindInterruption:
			errs = append(errs, Interrupted)
		}
	}
	return errs
}

// Failure returns err's typed failure when err is exactly one typed failure,
// the only shape a catch handles (rows.md, catch rule 6; law R6).
func Failure(err error) (error, bool) {
	switch e := err.(type) {
	case nil:
		return nil, false
	case *Cause:
		if e == nil {
			return err, true // a typed-nil *Cause is an ordinary typed failure (Add)
		}
		if len(e.Reasons) == 1 && e.Reasons[0].Kind == KindFailure {
			return e.Reasons[0].Err, true
		}
		return nil, false
	default:
		if err == Interrupted {
			return nil, false
		}
		return err, true
	}
}

// OnlyInterruption reports whether every reason in err is an interruption, the
// case in which main exits with status 130 (runtime.md, Exit causes rule 6).
func OnlyInterruption(err error) bool {
	if err == Interrupted {
		return true
	}
	c, ok := err.(*Cause)
	if !ok || c == nil || len(c.Reasons) == 0 {
		return false
	}
	for _, r := range c.Reasons {
		if r.Kind != KindInterruption {
			return false
		}
	}
	return true
}

// ExitCode is the process status for main's exit (runtime.md, Exit causes rule
// 6): 0 on success, 130 when every reason is an interruption, 1 otherwise.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case OnlyInterruption(err):
		return 130
	default:
		return 1
	}
}

// defectsOf keeps only the defects of err: the part of an abandoned task's
// exit that may never be dropped (laws R3, R6, R8).
func defectsOf(err error) error {
	e, ok := err.(*Cause)
	if !ok || e == nil {
		return nil
	}
	var c Cause
	for _, r := range e.Reasons {
		if r.Kind == KindDefect {
			c.Reasons = append(c.Reasons, r)
		}
	}
	return c.Err()
}
