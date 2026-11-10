package v1

import "errors"

// GoError is go.Error, the failure of a Go call whose last result is an
// error (lane G, G4; decision O14; docs/spec/interop.md, Go errors). It
// keeps Go's own error, so go.Is and go.As read its chain as errors.Is and
// errors.As do (IG5, #202). Its one Ergo field, Text, is the error's
// Error(), computed when it is read (Text): a failure nobody prints costs
// what hand Go's `return err` costs. The zero GoError is go.Error{} (Text
// "").
type GoError struct{ err error }

// Text is go.Error's field Text: the Go error's Error() at the time of the
// read, "" for the zero go.Error. The lowering reads the field as this
// method.
func (e GoError) Text() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

// Error is the failure's text: its Ergo name and field.
func (e GoError) Error() string { return "go.Error{Text:" + e.Text() + "}" }

// GoErrorOf is the failure of a Go call that returned err: nil for a nil
// err, else the go.Error that keeps err. A typed nil (a nil *T in a non-nil
// error) is not nil here, as in Go.
func GoErrorOf(err error) error {
	if err == nil {
		return nil
	}
	return GoError{err: err}
}

// GoErrorFrom is the go.Error of the Go error err, a sentinel variable of
// a binding (io.EOF): the target go.Is compares a chain with.
func GoErrorFrom(err error) GoError { return GoError{err: err} }

// GoErrorEagerOf is GoErrorOf of the reference lowering (the eager form G4
// shipped, kept selectable by lower.SetGoErrorEagerForTest): the text is
// computed at the failure and kept; the native error stays behind it for
// go.Is and go.As.
func GoErrorEagerOf(err error) error {
	if err == nil {
		return nil
	}
	return GoErrorEagerFrom(err)
}

// GoErrorEagerFrom is GoErrorFrom of the reference lowering.
func GoErrorEagerFrom(err error) GoError {
	if err == nil {
		return GoError{}
	}
	return GoError{err: eagerError{text: err.Error(), err: err}}
}

// Native is the Go error e keeps, which a Go `error` parameter gets: nil
// for the zero go.Error.
func (e GoError) Native() error { return e.native() }

// GoErrorsNative is the []error a Go `...error` parameter gets for a
// []go.Error.
func GoErrorsNative(es []GoError) []error {
	out := make([]error, len(es))
	for i, e := range es {
		out[i] = e.native()
	}
	return out
}

// eagerError is a native error with its text computed once.
type eagerError struct {
	text string
	err  error
}

func (e eagerError) Error() string { return e.text }

// native is the Go error a go.Error keeps: the eager form's wrapper is not
// part of the chain go.Is and go.As read.
func (e GoError) native() error {
	if w, ok := e.err.(eagerError); ok {
		return w.err
	}
	return e.err
}

// GoErrorText is go.Error{Text: text}, a go.Error an Ergo program builds:
// its native error is a text of its own, which go.Is finds equal to
// another go.Error built from the same text.
func GoErrorText(text string) GoError { return GoError{err: textError(text)} }

// textError is the native error of a go.Error an Ergo program builds.
type textError string

func (t textError) Error() string { return string(t) }

// GoIs is go.Is(e, target): errors.Is on the native errors.
func GoIs(e, target GoError) bool { return errors.Is(e.native(), target.native()) }

// GoAs is go.As[T](e): errors.As of the native error into a T. The checker
// admits only a T that implements error or is an interface (errors.As
// panics on any other).
func GoAs[T any](e GoError) (T, bool) {
	var t T
	ok := errors.As(e.native(), &t)
	return t, ok
}

// GoNilDefect is the defect of a call of import go whose Go function
// returned a nil pointer with a nil error where its binding promises a
// value: `(*S, error)` lowers to `go.Ref[S] fails go.Error`, and Go's
// convention that a nil error comes with a value is checked at the call,
// not assumed (docs/spec/interop.md, Lowering). At is the Ergo call's
// position, file:line:column.
type GoNilDefect struct {
	Func string
	At   string
}

// String is the defect's text, which the entry's report writes after
// "panic: ".
func (d *GoNilDefect) String() string {
	return "Go returned nil without an error at " + d.At + ": " + d.Func
}
