package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// denseUseSource calls std dense's Put, At and Rows, the operations whose
// lowered form docs/spec/stdlib.md (Dense stores 4) promises, and grows a
// table for the growth guard.
const denseUseSource = `import dense

func grow(n int) dense.Table[int] {
	let mut t = dense.New[int]()
	for i in 0..n {
		let _ = t.Put(i)
	}
	t
}

func sum(t dense.Table[int], id dense.ID[int]) int {
	let mut s = dense.At(t, id)
	for i, row in dense.Rows(t) {
		s += row + i
	}
	s
}

func main() {}
`

// denseFlatCallees are the only callees a promised dense operation's
// lowered body may name: conversions, len, the prelude's Append (Go's
// append), the defect of a full store, and ergo.Let, the identity that
// reads a let lowered as a Go constant (decls.go, letRead), which gc
// inlines. Any other call (a dictionary
// call, a Map read, a helper the body reaches through) breaks the promise.
var denseFlatCallees = map[string]bool{"int": true, "uint32": true, "len": true, "cap": true, "append": true, "Append": true, "panic": true, "ergo.UnreachableAt": true, "ergo.Let": true}

// denseFlatOnce is the call Put makes once for a table of rows of no size
// (Dense stores 4, SL2b): it takes room for every id.
const denseFlatOnce = "WithCapacity"

// TestDenseLowersFlat holds the one promised fact of Dense stores 4 on the
// lowered Go, in process: Put, At and Rows lower to an append, a slice
// index and a field read, with no call but those of denseFlatCallees, no
// environment and no poll. It kills dense-at-through-get.
func TestDenseLowersFlat(t *testing.T) {
	code := mustLower(t, "denseflat", denseUseSource).mainGo()
	for _, name := range []string{"Put", "At", "Rows"} {
		body := goFuncBody(t, code, name)
		if strings.Contains(body, "env") || strings.Contains(body, ".Check()") {
			t.Errorf("dense %s reads an environment or polls:\n%s", name, body)
		}
		file, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+body, 0)
		if err != nil {
			t.Fatalf("dense %s: %v\n%s", name, err, body)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fun := call.Fun
			if ix, ok := fun.(*ast.IndexExpr); ok {
				fun = ix.X
			}
			var callee string
			switch f := fun.(type) {
			case *ast.Ident:
				callee = f.Name
			case *ast.SelectorExpr:
				if x, ok := f.X.(*ast.Ident); ok {
					callee = x.Name + "." + f.Sel.Name
				}
			}
			if name == "Put" && strings.HasSuffix(callee, "."+denseFlatOnce) {
				return true
			}
			if !denseFlatCallees[callee] {
				t.Errorf("dense %s calls %s; its promised form calls none:\n%s", name, callee, body)
			}
			return true
		})
	}
}

// denseGrowthDriver counts the bytes grow allocates for n and for 2n rows:
// Put appends in amortized constant space, so 2n rows cost at most three
// times n rows (twice, plus the slack of Go's growth steps), where a Put
// that copies the table costs four times.
const denseGrowthDriver = `package main
import (
	"reflect"
	"runtime"
	"testing"
)
func bytesOf(n int) uint64 {
	f := reflect.ValueOf(grow)
	args := []reflect.Value{reflect.ValueOf(n)}
	if f.Type().NumIn() == 2 {
		args = append([]reflect.Value{reflect.Zero(f.Type().In(0))}, args...)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out := f.Call(args)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(out)
	return after.TotalAlloc - before.TotalAlloc
}
func TestPutGrowsLinearly(t *testing.T) {
	const n = 1 << 12
	one, two := bytesOf(n), bytesOf(2*n)
	if two > 3*one {
		t.Fatalf("Put of %d rows allocated %d bytes, of %d rows %d bytes: more than three times", n, one, 2*n, two)
	}
}
`

// denseFlatSource puts n rows of no size in a table and returns the room
// of its rows: Go grows a full slice of rows of no size to exactly one
// more row, so a table that grows that way has room for n rows, and one
// that took room for every id has room for 4294967295.
const denseFlatSource = `import dense

extern go cap func room[T](xs []T) int

type tag {}

func roomAfter(n int) int {
	let mut t = dense.New[tag]()
	for _ in 0..n {
		let _ = t.Put(tag)
	}
	room(dense.Rows(t))
}

func intRoomAfter(n int) int {
	let mut t = dense.New[int]()
	for i in 0..n {
		let _ = t.Put(i)
	}
	room(dense.Rows(t))
}

func main() {}
`

const denseFlatDriver = `package main
import (
	"reflect"
	"testing"
)
func TestFlatTableTakesRoomOnce(t *testing.T) {
	f := reflect.ValueOf(roomAfter)
	args := []reflect.Value{reflect.ValueOf(4096)}
	if f.Type().NumIn() == 2 {
		args = append([]reflect.Value{reflect.Zero(f.Type().In(0))}, args...)
	}
	if got := f.Call(args)[0].Int(); got != 1<<32-1 {
		t.Fatalf("after 4096 Puts of rows of no size the table has room for %d rows; want 4294967295, room taken once, so no later Put grows it", got)
	}
	// Rows with a size never take that room: only rows of no size grow to
	// exactly one more than they hold.
	g := reflect.ValueOf(intRoomAfter)
	for n := 0; n <= 600; n++ {
		args := []reflect.Value{reflect.ValueOf(n)}
		if g.Type().NumIn() == 2 {
			args = append([]reflect.Value{reflect.Zero(g.Type().In(0))}, args...)
		}
		if got := g.Call(args)[0].Int(); got > int64(4*n+16) {
			t.Fatalf("after %d Puts of int rows the table has room for %d rows: a table of rows with a size took the room of rows of no size", n, got)
		}
	}
}
`

// TestDenseFlatTableRegrowsOnce is the growth guard of a table of rows of
// no size (Dense stores 4, SL2b): Go grows such a slice on every append, so
// a Put that does not take room for every id grows it on each of 4096
// Puts. It reads the room the program's table has at the end: room for
// every id, taken once, so no Put after it calls growslice; and that a
// table of int rows, at every length to 600, never takes that room. It
// kills dense-flat-grows and builtin-cap-copies (cap of a copy took an int
// table for one of rows of no size).
//
// It builds Go because the room of a slice is the built program's
// (Go's growslice); no in-process check observes it.
func TestDenseFlatTableRegrowsOnce(t *testing.T) {
	needsBuild(t)
	goTestEmitted(t, mustLower(t, "denseflatgrow", denseFlatSource), denseFlatDriver)
}

// TestDensePutGrowsLinearly is the growth guard of Dense stores 4: it
// counts the bytes Put allocates at n and 2n rows. It kills
// dense-put-copies.
//
// It builds Go because it counts the allocations of the built program
// (runtime.MemStats); no in-process check observes them.
func TestDensePutGrowsLinearly(t *testing.T) {
	needsBuild(t)
	goTestEmitted(t, mustLower(t, "densegrowth", denseUseSource), denseGrowthDriver)
}

// denseStringsSource interns n words twice each in a dense.Strings.
const denseStringsSource = `import dense

func words(n int) dense.Strings {
	let mut s = dense.NewStrings()
	for _ in 0..2 {
		for i in 0..n {
			let _ = s.InternText("w\(i)")
		}
	}
	s
}

func main() {}
`

// denseStringsDriver counts the bytes and allocations of interning n words
// twice each, in dense.Strings and in a hand-Go map[string]uint32, at n
// and 2n, and prints them as receipt lines. Strings at 2n cost at most
// three times n (the growth guard); the comparison with the map pins no
// bound.
const denseStringsDriver = `package main
import (
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"testing"
)
func count(f func()) (uint64, uint64) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, after.Mallocs - before.Mallocs
}
func ergoWords(n int) func() {
	return func() {
		f := reflect.ValueOf(words)
		args := []reflect.Value{reflect.ValueOf(n)}
		if f.Type().NumIn() == 2 {
			args = append([]reflect.Value{reflect.Zero(f.Type().In(0))}, args...)
		}
		runtime.KeepAlive(f.Call(args))
	}
}
func goWords(n int) func() {
	return func() {
		m := map[string]uint32{}
		for round := 0; round < 2; round++ {
			for i := 0; i < n; i++ {
				w := "w" + strconv.Itoa(i)
				if _, ok := m[w]; !ok {
					m[w] = uint32(len(m))
				}
			}
		}
		runtime.KeepAlive(m)
	}
}
func TestStringsCounts(t *testing.T) {
	const n = 1 << 12
	var bytes [2]uint64
	for k, size := range []int{n, 2 * n} {
		eb, em := count(ergoWords(size))
		gb, gm := count(goWords(size))
		bytes[k] = eb
		fmt.Printf("receipt strings n=%d dense.Strings bytes=%d allocs=%d go-map bytes=%d allocs=%d\n", size, eb, em, gb, gm)
	}
	if bytes[1] > 3*bytes[0] {
		t.Fatalf("Strings of %d words allocated %d bytes, of %d words %d bytes: more than three times", n, bytes[0], 2*n, bytes[1])
	}
}
`

// TestDenseStringsCounts is Strings' counted receipt and growth guard
// (Dense stores 4): the bytes and allocations of interning n and 2n words
// in dense.Strings and in a hand-Go map[string]uint32, logged, with
// Strings' bytes at 2n at most three times n. It kills
// dense-strings-settles-every-text.
//
// It builds Go because it counts the allocations of the built program
// (runtime.MemStats); no in-process check observes them.
func TestDenseStringsCounts(t *testing.T) {
	needsBuild(t)
	out := goTestEmittedVerbose(t, mustLower(t, "densestrings", denseStringsSource), denseStringsDriver)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "receipt ") {
			t.Log(line)
		}
	}
}
