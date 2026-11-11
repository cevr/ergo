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
// Every callback parameter of a listed function has the same clause.
// Values are per-function source reasons; an empty reason grants no trust.
var GoSyncCallbacks = map[string]string{
	"bytes.ContainsFunc":                 "bytes/bytes.go: calls IndexFunc synchronously; no callback is stored",
	"bytes.FieldsFunc":                   "bytes/bytes.go: invokes f in its field-scanning loop; stores only byte spans",
	"bytes.IndexFunc":                    "bytes/bytes.go: invokes f in its rune-scanning loop; no retained callback",
	"bytes.LastIndexFunc":                "bytes/bytes.go: invokes f in its reverse rune scan; no retained callback",
	"bytes.Map":                          "bytes/bytes.go: invokes mapping per rune and returns bytes, not the callback",
	"bytes.TrimFunc":                     "bytes/bytes.go: calls the two synchronous trim scans",
	"bytes.TrimLeftFunc":                 "bytes/bytes.go: invokes f in a rune scan and keeps no callback",
	"bytes.TrimRightFunc":                "bytes/bytes.go: invokes f in a reverse rune scan and keeps no callback",
	"strings.ContainsFunc":               "strings/strings.go: calls IndexFunc synchronously; no callback is stored",
	"strings.FieldsFunc":                 "strings/strings.go: invokes f in the field scan and stores only spans",
	"strings.IndexFunc":                  "strings/strings.go: invokes f in the rune scan and keeps no callback",
	"strings.LastIndexFunc":              "strings/strings.go: invokes f in the reverse rune scan and keeps no callback",
	"strings.Map":                        "strings/strings.go: invokes mapping per rune and returns a string",
	"strings.TrimFunc":                   "strings/strings.go: calls the two synchronous trim scans",
	"strings.TrimLeftFunc":               "strings/strings.go: invokes f in a rune scan and keeps no callback",
	"strings.TrimRightFunc":              "strings/strings.go: invokes f in a reverse rune scan and keeps no callback",
	"slices.BinarySearchFunc":            "slices/sort.go: cmp runs only in the binary-search loop",
	"slices.CompactFunc":                 "slices/slices.go: eq runs only in the compaction loop",
	"slices.CompareFunc":                 "slices/slices.go: cmp runs only in the comparison loop",
	"slices.ContainsFunc":                "slices/slices.go: calls the synchronous IndexFunc",
	"slices.DeleteFunc":                  "slices/slices.go: del runs only in the deletion loop",
	"slices.EqualFunc":                   "slices/slices.go: eq runs only in the equality loop",
	"slices.IndexFunc":                   "slices/slices.go: f runs only in the index scan",
	"slices.IsSortedFunc":                "slices/sort.go: cmp runs only in the sortedness scan",
	"slices.MaxFunc":                     "slices/sort.go: cmp runs only in the extremum scan",
	"slices.MinFunc":                     "slices/sort.go: cmp runs only in the extremum scan",
	"slices.SortFunc":                    "slices/sort.go and zsortanyfunc.go: pdqsort calls cmp synchronously and stores no callback",
	"slices.SortStableFunc":              "slices/sort.go and zsortanyfunc.go: stable sort calls cmp synchronously and stores no callback",
	"slices.SortedFunc":                  "slices/iter.go: collects the input then calls SortFunc before returning",
	"slices.SortedStableFunc":            "slices/iter.go: collects the input then calls SortStableFunc before returning",
	"maps.DeleteFunc":                    "maps/maps.go: del runs only in the deletion loop",
	"maps.EqualFunc":                     "maps/maps.go: eq runs only in the equality loop",
	"sort.Find":                          "sort/search.go: cmp runs only in the search loop and final comparison",
	"sort.Search":                        "sort/search.go: f runs only in the search loop",
	"math/rand.Shuffle":                  "math/rand/rand.go: delegates to Rand.Shuffle synchronously",
	"math/rand.Rand.Shuffle":             "math/rand/rand.go: swap runs only in the Fisher-Yates loop",
	"math/rand/v2.Shuffle":               "math/rand/v2/rand.go: delegates to Rand.Shuffle synchronously",
	"math/rand/v2.Rand.Shuffle":          "math/rand/v2/rand.go: swap runs only in the Fisher-Yates loop",
	"os.Expand":                          "os/env.go: mapping runs only in the expansion scan; only the string result is stored",
	"path/filepath.Walk":                 "path/filepath/path.go: recursive walk returns before Walk returns; no callback is stored",
	"path/filepath.WalkDir":              "path/filepath/path.go: recursive walkDir returns before WalkDir returns; no callback is stored",
	"io/fs.WalkDir":                      "io/fs/walk.go: recursive walkDir returns before WalkDir returns; no callback is stored",
	"regexp.Regexp.ReplaceAllFunc":       "regexp/regexp.go: replacement callback runs only inside replaceAll",
	"regexp.Regexp.ReplaceAllStringFunc": "regexp/regexp.go: replacement callback runs only inside replaceAll",
	"flag.Visit":                         "flag/flag.go: delegates to CommandLine.Visit synchronously",
	"flag.VisitAll":                      "flag/flag.go: delegates to CommandLine.VisitAll synchronously",
	"flag.FlagSet.Visit":                 "flag/flag.go: invokes fn over the sorted actual flags and stores no callback",
	"flag.FlagSet.VisitAll":              "flag/flag.go: invokes fn over the sorted formal flags and stores no callback",
	"go/ast.Inspect":                     "go/ast/walk.go: Walk calls the visitor synchronously over the syntax tree",
	"expvar.Do":                          "expvar/expvar.go: invokes f over the variables under the call's lock and stores no callback",
	"log/slog.Record.Attrs":              "log/slog/record.go: invokes f over the record's attributes and stores no callback",
	"sync.Once.Do":                       "sync/once.go: doSlow runs f synchronously under the lock and retains only the done flag",
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
