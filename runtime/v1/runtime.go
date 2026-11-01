// Package v1 is the runtime support that lowered Ergo programs link. The
// lowering (internal/sketch/lower) copies this package's source into every
// emitted module, beside the generated main package, so a program needs no
// module download and its build key covers these exact bytes.
//
// It holds only what lowered code cannot express as ordinary Go: the host's
// providers (Console), the Go bodies of prelude extern declarations
// (Sleep), how main reports a typed failure, and the text of a basic value
// inside an error type's text. A defect is an ordinary Go panic: nothing in
// this file recovers one, so Go's own report names the panicking frame,
// which the //line directives map to the .ergo source.
//
// The bodies of the std packages task, scope, queue and slice are in
// task.go, queue.go and data.go, over runtime/rt; exit.go is the exit
// vocabulary they share and the entry, Main, which records a defect with
// its stack at a task or frame boundary. Their contract is
// docs/spec/runtime.md (Tasks, Exit actions, Queues). The std signatures
// in internal/sketch/std bind them (docs/spec/stdlib.md, v1 packages and
// Hidden arguments). The lowering of those calls (check-v1 stage D) copies
// this package and runtime/rt into a module; until then SourceFiles names
// this file alone.
//
// Prior art: Gleam's prelude.mjs and ReScript's runtime modules ship the
// small host-language support that generated code calls; Borgo emits Go
// that links a small runtime package.
package v1

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// Console is the host's Console provider, which main binds: Println writes
// one line to standard output.
type Console struct{}

// Println writes line and a line feed to standard output in one write, so
// lines from concurrent writers do not interleave within a line.
func (Console) Println(line string) {
	_, _ = os.Stdout.WriteString(line + "\n")
}

// Sleep is the Go body of the prelude's `extern go
// ergo.local/prototype/runtime/rt.Sleep func Sleep(s LiveScheduler, d
// duration)`. The hidden task is the task running the call. The primitive
// observes its stop before and during the wait, and respects its mask.
// Interruption follows the same exit path as Wait and queue operations.
func Sleep(t *rt.Task, d time.Duration) {
	if err := rt.Sleep(t, rt.Live{}, d); err != nil {
		raise(err)
	}
}

// Fail reports main's typed failure on standard error and exits with
// status 1 (lower-v1 D3: a remaining typed failure is printed, nonzero).
func Fail(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

// The text of an error type's value comes from its fields (types.md,
// Methods 6). The lowering generates, for each type an error's text reaches,
// a function that writes the value field by field; these functions write
// the basic values in it. They take Go's basic types, so no method of a
// user type (a 196m String or Error) can change the text.

// TextBool is a bool's text: true or false.
func TextBool(b bool) string { return strconv.FormatBool(b) }

// TextInt is a signed integer's text, in decimal.
func TextInt(i int64) string { return strconv.FormatInt(i, 10) }

// TextUint is an unsigned integer's text, in decimal.
func TextUint(u uint64) string { return strconv.FormatUint(u, 10) }

// TextFloat is a float's text in the shortest form that reads back as the
// same value of its width, bits (32 or 64), as Go's %v writes it.
func TextFloat(f float64, bits int) string { return strconv.FormatFloat(f, 'g', -1, bits) }

// TextDuration is a duration's text, as Go writes a time.Duration: 1.5s.
func TextDuration(d time.Duration) string { return d.String() }

// TextList is a list's text from its elements' texts: [a b c].
func TextList(elems []string) string { return "[" + strings.Join(elems, " ") + "]" }
