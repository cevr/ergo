package testinputs

import (
	"errors"
	"io"
)

// ExecSpec is a command for Exec to run under observation.
type ExecSpec struct {
	// Argv is the command and its arguments; Argv[0] is looked up on the
	// PATH of Env, under the filter.
	Argv []string
	// Env is the command's whole environment.
	Env []string
	// Dir is its working directory.
	Dir string
	// Stdout and Stderr receive its output; nil discards it.
	Stdout, Stderr io.Writer
}

// ExecResult is what an observed command did.
type ExecResult struct {
	// Exit is the command's exit code (128 + the signal for a signal).
	Exit int
	// Reason is why the observation is incomplete, or "": a result with
	// a reason must not be reused.
	Reason string
	// Inputs are accesses to absolute paths the command depended on.
	Inputs []Input
}

// SnapKind distinguishes a file read or directory open from a lookup.
type SnapKind uint8

const (
	SnapRead SnapKind = iota + 1
	SnapLookup
)

// Input is an access to an absolute path.
type Input struct {
	Path string
	Kind SnapKind
}

// ErrNoObserver is Exec's error where no observer runs (another operating
// system or architecture): the command must run unkeyed.
var ErrNoObserver = errors.New("testinputs: no observer on this platform")
