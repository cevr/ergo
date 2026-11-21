package lower

import (
	"runtime"
	"slices"
	"testing"
)

// TestClosureTypesStd (LBK-TYPED-WALK): the closure walk types Go's
// standard library, so a call of a std type's method (atomic.Int64's Load)
// resolves to std and takes no method of that name from the world, while
// a call through a std interface (io.Writer's Write) may run any method of
// its name and still takes each. Without std types the first call takes
// Box.Load by name, and with an interface's call resolved like a method's
// the second misses Sink.Write: the key misses the code a run reaches.
//
// Mutants (lower mutants.json): ltw-std-untyped, ltw-std-interface-dropped.
func TestClosureTypesStd(t *testing.T) {
	const src = `package a

import (
	"io"
	"sync/atomic"
)

type Box struct{ n int64 }

func (b *Box) Load() int64 { return b.n }

type Sink struct{}

func (Sink) Write(p []byte) (int, error) { return len(p), nil }

var count atomic.Int64

func Count() int64 { return count.Load() }

func Emit(w io.Writer) { _, _ = w.Write(nil) }
`
	w := newGoWorld(func(p string) ([]GoSourceFile, bool) {
		if p != "ex.test/a" {
			return nil, false
		}
		return []GoSourceFile{{Name: "a.go", Data: []byte(src)}}, true
	})
	w.std = stdTypesOf(GoTarget{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
	if w.std.ctx.GOROOT == "" {
		t.Skip("no GOROOT: std is not typed")
	}
	for _, c := range []struct {
		fn, decl string
		want     bool
		why      string
	}{
		{"Count", "ex.test/a Box.Load", false, "count.Load is atomic.Int64's method"},
		{"Emit", "ex.test/a Sink.Write", true, "w.Write may run any Write"},
	} {
		got := slices.Contains(w.closure("ex.test/a", c.fn).Decls, c.decl)
		if got != c.want {
			t.Errorf("%s's closure holds %s: %v; want %v: %s", c.fn, c.decl, got, c.want, c.why)
		}
	}
}
