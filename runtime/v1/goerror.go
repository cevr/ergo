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
