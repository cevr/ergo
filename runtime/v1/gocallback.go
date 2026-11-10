package v1

import "errors"

// GoSyncCallbacks are the Go functions and methods of the standard library
// that call each of their function parameters only during the call, on
// the caller's goroutine, and keep none of them, reviewed against their
// source at go1.27 (IG6; docs/spec/interop.md, Callbacks). Their bindings
// write a callback as a plain (non-escaping) function type whose rows are
// the call's: it may need what its caller has, and when Go's callback
// returns an error, fail with any row, which the call then fails with.
// Every other Go callback is escaping: it may run after the call, so its
// binding writes `escaping` and no row beyond go.Error. Keyed by "path.F"
// or "path.T.M". Trust facts no tool can derive come only from reviewed
// tables (owner q36).
var GoSyncCallbacks = map[string]bool{
	"bytes.ContainsFunc": true, "bytes.FieldsFunc": true, "bytes.IndexFunc": true, "bytes.LastIndexFunc": true,
	"bytes.Map": true, "bytes.TrimFunc": true, "bytes.TrimLeftFunc": true, "bytes.TrimRightFunc": true,
	"strings.ContainsFunc": true, "strings.FieldsFunc": true, "strings.IndexFunc": true, "strings.LastIndexFunc": true,
	"strings.Map": true, "strings.TrimFunc": true, "strings.TrimLeftFunc": true, "strings.TrimRightFunc": true,
	"slices.BinarySearchFunc": true, "slices.CompactFunc": true, "slices.CompareFunc": true, "slices.ContainsFunc": true,
	"slices.DeleteFunc": true, "slices.EqualFunc": true, "slices.IndexFunc": true, "slices.IsSortedFunc": true,
	"slices.MaxFunc": true, "slices.MinFunc": true, "slices.SortFunc": true, "slices.SortStableFunc": true,
	"slices.SortedFunc": true, "slices.SortedStableFunc": true,
	"maps.DeleteFunc": true, "maps.EqualFunc": true,
	"sort.Find": true, "sort.Search": true,
	"math/rand.Shuffle": true, "math/rand.Rand.Shuffle": true, "math/rand/v2.Shuffle": true, "math/rand/v2.Rand.Shuffle": true,
	"os.Expand":                          true,
	"path/filepath.Walk":                 true,
	"path/filepath.WalkDir":              true,
	"io/fs.WalkDir":                      true,
	"regexp.Regexp.ReplaceAllFunc":       true,
	"regexp.Regexp.ReplaceAllStringFunc": true,
	"flag.Visit":                         true, "flag.VisitAll": true, "flag.FlagSet.Visit": true, "flag.FlagSet.VisitAll": true,
	"go/ast.Inspect":        true,
	"expvar.Do":             true,
	"log/slog.Record.Attrs": true,
	"sync.Once.Do":          true,
}

// GoTunnel carries an Ergo failure through Go code: the error a callback
// returns to Go when its failure is not a go.Error. The Go function that
// called it returns it (filepath.WalkDir returns the callback's error),
// and GoUntunnel finds it there, so the call fails with the Ergo failure,
// typed.
type GoTunnel struct{ Err error }

func (t *GoTunnel) Error() string { return "Ergo failure " + t.Err.Error() + " through Go" }

// GoCallbackError is the error a callback whose Go type returns one gives
// Go for the Ergo failure err (nil when it did not fail): a go.Error is Go's
// own error, so Go code sees what a Go function returned (fs.SkipDir
// skips); any other failure, and a go.Error with no Go error, is tunnelled.
func GoCallbackError(err error) error {
	if err == nil {
		return nil
	}
	if g, ok := err.(GoError); ok && g.native() != nil {
		return g.native()
	}
	return &GoTunnel{Err: err}
}

// GoUntunnel is the failure of a Go call that took a callback: the Ergo
// failure a callback tunnelled, when Go's error chain holds one, else
// GoErrorOf(err).
func GoUntunnel(err error) error {
	if err == nil {
		return nil
	}
	var t *GoTunnel
	if errors.As(err, &t) {
		return t.Err
	}
	return GoErrorOf(err)
}

// GoUntunnelEager is GoUntunnel of the eager reference lowering.
func GoUntunnelEager(err error) error {
	if err == nil {
		return nil
	}
	var t *GoTunnel
	if errors.As(err, &t) {
		return t.Err
	}
	return GoErrorEagerOf(err)
}
