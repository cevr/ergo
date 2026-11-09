package v1

// GoError is go.Error, the failure of a user extern whose Go target returns
// an error (lane G, G4; decision O14): Text is the error's Error(). The
// lowering binds the predeclared go.Error to this type, and turns a
// target's non-nil error into it with GoErrorOf.
type GoError struct{ Text string }

// Error is the failure's text: its Ergo name and field.
func (e GoError) Error() string { return "go.Error{Text:" + e.Text + "}" }

// GoErrorOf is the failure of a user extern call whose Go target returned
// err: nil for a nil err, else GoError{Text: err.Error()}. The Go error
// itself is not kept: Ergo has no errors.Is yet, so nothing could read it.
// A typed nil (a nil *T in a non-nil error) is not nil here, as in Go, and
// its Error() runs.
func GoErrorOf(err error) error {
	if err == nil {
		return nil
	}
	return GoError{Text: err.Error()}
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
