package screen

import (
	"errors"
	"fmt"
	"sync"

	"ergo.local/prototype/hosts/opentui/ot"
)

// The screen's failure algebra. Every error an exported function or
// method of this package returns is a *Failure: the call that failed, the
// node it was about, and the Go error that says why, kept whole (Unwrap),
// so errors.As reaches the library's ot.Status and errors.Is the
// sentinels below. A read (Rows, Cell, BoxOf, ScrollOf, HitTest, Feed,
// Settle) has no failure: it panics only when the owner thread is gone,
// which is after Quit, a defect of the caller.

// The calls a Failure names.
const (
	// CallOpen: Headless, Open or OpenOn (the context, the session, the
	// renderer, the terminal's setup).
	CallOpen = iota
	// CallScene: a scene command of the batch (Create, Move, Destroy,
	// SetText, SetStyle, SetLook, Focus, SetPlaceholder, SetPixels).
	CallScene
	// CallFrame: painting, laying out or committing the frame.
	CallFrame
	// CallOutput: reading the session's output or writing it to the
	// terminal.
	CallOutput
	// CallScroll: Reveal or ScrollTo.
	CallScroll
	// CallEdit: Edit.
	CallEdit
	// CallNotify: Notify.
	CallNotify
	// CallResize: Resize.
	CallResize
	// CallInput: Next.
	CallInput
	// CallQuit: Quit.
	CallQuit
)

// Failure is a screen call that failed.
type Failure struct {
	// Call is the call (CallOpen to CallQuit).
	Call int
	// Node is the scene node the call was about, 0 for none.
	Node int
	// Err says why.
	Err error
}

func (f *Failure) Error() string { return f.Err.Error() }

func (f *Failure) Unwrap() error { return f.Err }

// Status is the library's status code that refused the call, 0 when the
// failure is Go's own (a write to the terminal, a closed screen).
func (f *Failure) Status() int {
	var st ot.Status
	if errors.As(f.Err, &st) {
		return int(st)
	}
	return 0
}

// The failures with no Go cause.
var (
	// ErrFrameFailed: the library could not output the frame.
	ErrFrameFailed = errors.New("screen: the frame failed (larger than the output queue, or encoding failed)")
	// ErrFrameSkipped: the frame was skipped twice under output pressure.
	ErrFrameSkipped = errors.New("screen: the frame was skipped twice under output pressure")
	// ErrUnsettled: the session did not settle.
	ErrUnsettled = errors.New("screen: the session did not settle")
	// ErrZeroSize: the terminal reports a size of zero.
	ErrZeroSize = errors.New("the terminal reports a size of zero")
)

// wrap is fail of err, nil for nil.
func wrap(call, node int, err error) error {
	if err == nil {
		return nil
	}
	return fail(call, node, err)
}

// fail is the error err (not nil) as a Failure of call about node. When
// err holds a Failure already, the innermost call names the failure: err
// itself when it is that Failure, else a Failure of its call and node
// over err's text. Every return is a non-nil error, so the binder proves
// Headless's and Open's screen is there whenever no error is (gobind,
// nonnil.go).
func fail(call, node int, err error) error {
	if f, ok := err.(*Failure); ok {
		return f
	}
	var in *Failure
	if errors.As(err, &in) {
		return &Failure{Call: in.Call, Node: in.Node, Err: err}
	}
	return &Failure{Call: call, Node: node, Err: err}
}

// Fault injection and the process's screen count, for the programs that
// test a driver's failure paths (hosts/opentui's hostagree and failterm):
// a check stands at the start of each step that can fail, and FailAt
// makes one of them fail as the library would.

// Check is one check the screens of this process passed: its call and
// node.
type Check struct {
	Call, Node int
}

var faults struct {
	sync.Mutex
	at     int
	status int
	checks []Check
	live   int
}

// FailAt starts a new count of checks (Checks is empty after it) and
// makes the at-th check from here (counted from 1 over every screen) fail
// with the library status status (an ot.Status); at 0 fails none.
func FailAt(at, status int) {
	faults.Lock()
	defer faults.Unlock()
	faults.at, faults.status, faults.checks = at, status, nil
}

// Checks is every check this process's screens passed or failed, in
// order.
func Checks() []Check {
	faults.Lock()
	defer faults.Unlock()
	return append([]Check(nil), faults.checks...)
}

// Live is how many screens this process made and has not quit.
func Live() int {
	faults.Lock()
	defer faults.Unlock()
	return faults.live
}

// check is the check of a step of call about node: the Failure FailAt
// asked for, or nil.
func check(call, node int) error {
	faults.Lock()
	defer faults.Unlock()
	faults.checks = append(faults.checks, Check{call, node})
	if len(faults.checks) != faults.at {
		return nil
	}
	return &Failure{Call: call, Node: node, Err: fmt.Errorf("screen: check %d: %w", faults.at, ot.Status(faults.status))}
}

// made counts a screen made (by) 1 or quit (-1).
func made(by int) {
	faults.Lock()
	faults.live += by
	faults.Unlock()
}
