package lower

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// updateAllocSource has record updates in hot pure functions: one field, a
// field value that is a call (its store follows the copy), a nested update
// and a generic record. Each is called directly from Go below; the three
// whose first parameter is Walk are Go methods of Walk (196m).
const updateAllocSource = `type Walk { At int, Tide int, Turn int, Shells int, Left string }

type Box[T] { V T, N int }

func inc(n int) int { n + 1 }

func step(w Walk) Walk {
	Walk{..w, Turn: w.Turn + 1}
}

func collect(w Walk) Walk {
	Walk{..w, Shells: inc(w.Shells), At: w.At + w.Tide}
}

func twice(w Walk) Walk {
	Walk{..Walk{..w, Turn: 1}, Tide: w.Tide + 2, Turn: 2}
}

func rebox(b Box[int]) Box[int] {
	Box{..b, N: b.N + 1}
}

type Pos { At int, Tide int }

type Moving { ..Pos, Turn int, Left string }

type Stopped { ..Pos, Shells int }

func halt(m Moving) Stopped {
	Stopped{..m, Shells: m.Turn}
}

func main() needs Console {
	let w = Walk{At: 1, Tide: 2, Turn: 3, Shells: 4, Left: "abc"}
	Console.Println("\(step(w).Turn) \(collect(w).Shells) \(twice(w).Tide) \(rebox(Box{V: 1, N: 2}).N) \(halt(Moving{At: 1, Tide: 2, Turn: 3, Left: "x"}).Shells)")
}
`

// updateAllocDriver calls each update from Go, beside the same function
// written by hand as Go's copy-then-assign idiom, and counts allocations:
// both must be 0. The benchmarks are the receipt's ns/op (run with -v).
const updateAllocDriver = `package main

import "testing"

var sink Walk
var boxSink Box[int]
var stopSink Stopped

func stepHand(w Walk) Walk {
	n := w
	n.Turn = w.Turn + 1
	return n
}

func haltHand(m Moving) Stopped {
	return Stopped{At: m.At, Tide: m.Tide, Shells: m.Turn}
}

func TestRecordUpdateAllocsDriver(t *testing.T) {
	w := Walk{At: 1, Tide: 2, Turn: 3, Shells: 4, Left: "abc"}
	mv := Moving{At: 1, Tide: 2, Turn: 3, Left: "abc"}
	for name, f := range map[string]func(){
		"step":     func() { sink = w.step() },
		"collect":  func() { sink = w.collect() },
		"twice":    func() { sink = w.twice() },
		"rebox":    func() { boxSink = rebox(boxSink) },
		"stepHand": func() { sink = stepHand(w) },
		"halt":     func() { stopSink = mv.halt() },
		"haltHand": func() { stopSink = haltHand(mv) },
	} {
		if n := testing.AllocsPerRun(200, f); n != 0 {
			t.Errorf("%s allocates %v times per call", name, n)
		}
	}
	if s := w.step(); s != (Walk{At: 1, Tide: 2, Turn: 4, Shells: 4, Left: "abc"}) || w.Turn != 3 {
		t.Errorf("w.step() = %+v, w = %+v", s, w)
	}
	if c := w.collect(); c != (Walk{At: 3, Tide: 2, Turn: 3, Shells: 5, Left: "abc"}) {
		t.Errorf("w.collect() = %+v", c)
	}
	if d := w.twice(); d != (Walk{At: 1, Tide: 4, Turn: 2, Shells: 4, Left: "abc"}) {
		t.Errorf("w.twice() = %+v", d)
	}
	if s := mv.halt(); s != (Stopped{At: 1, Tide: 2, Shells: 3}) {
		t.Errorf("mv.halt() = %+v", s)
	}
	if r := rebox(Box[int]{V: 7, N: 1}); r != (Box[int]{V: 7, N: 2}) {
		t.Errorf("rebox = %+v", r)
	}
	update := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			sink = sink.step()
		}
	})
	hand := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			sink = stepHand(sink)
		}
	})
	t.Logf("receipt: update %s; hand-written %s", update.String()+" "+update.MemString(), hand.String()+" "+hand.MemString())
	spread := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			stopSink = mv.halt()
		}
	})
	spreadHand := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			stopSink = haltHand(mv)
		}
	})
	t.Logf("receipt: spread %s; hand-written %s", spread.String()+" "+spread.MemString(), spreadHand.String()+" "+spreadHand.MemString())
}
`

// TestRecordUpdateAllocs (record-update, spec Design 4): a record update
// lowers to a copy of its base and one store per field, a Go value with no
// pointer, so it allocates nothing, as the hand-written Go does. The
// emitted functions are called directly from Go beside the emitted code
// (testing.AllocsPerRun), and their bodies take no address and call no
// new: a heap copy that escape analysis happens to keep on the stack is a
// lowering the contract refuses too.
func TestRecordUpdateAllocs(t *testing.T) {
	prog := mustLower(t, "update_alloc", updateAllocSource)
	main := prog.mainGo()
	for _, fn := range []string{"step", "collect", "twice", "rebox", "halt"} {
		body := goFuncBody(t, main, fn)
		if strings.Contains(body, "new(") || strings.Contains(body, "&") {
			t.Errorf("the update in %s takes an address or calls new:\n%s", fn, body)
		}
	}
	out := goTestEmittedVerbose(t, prog, updateAllocDriver)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "receipt:") {
			t.Log(strings.TrimSpace(l))
		}
	}
}

// updateOrderSource prints the order of evaluation: the base once and
// first, then the field values in source order, also for a nested base, a
// base inside a call's arguments and an update in a loop condition.
const updateOrderSource = `type Walk { At int, Turn int, Shells int, Left string }

func mk(tag string) Walk needs Console {
	Console.Println("base " + tag)
	Walk{At: 1, Turn: 2, Shells: 3, Left: "abc"}
}

func val(n int) int needs Console {
	Console.Println("value \(n)")
	n
}

func show(w Walk) string { "\(w.At) \(w.Turn) \(w.Shells) \(w.Left)" }

func pair(a int, w Walk) int { a + w.Turn }

func main() needs Console {
	let e = Walk{..mk("e"), Turn: val(7), Shells: val(8)}
	Console.Println(show(e))
	let n = Walk{..Walk{..mk("n"), At: val(1)}, Turn: val(2)}
	Console.Println(show(n))
	Console.Println("\(pair(val(10), Walk{..mk("p"), Turn: val(11)}))")
	let mut w = Walk{At: 0, Turn: 0, Shells: 0, Left: ""}
	for (Walk{..w, Turn: w.Turn + 1}).Turn < 3 {
		w = Walk{..w, Turn: w.Turn + 1, Left: w.Left + "x"}
	}
	Console.Println(show(w))
}
`

const updateOrderStdout = `base e
value 7
value 8
1 7 8 abc
base n
value 1
value 2
1 2 3 abc
value 10
base p
value 11
21
0 2 0 xx
`

// TestRecordUpdateRuns runs updates against their meaning: the order
// program above, and a seeded property whose oracle is its own model of
// the update (a copy of the base with the named fields replaced, the later
// of two nested updates winning, the base unchanged).
func TestRecordUpdateRuns(t *testing.T) {
	runBehaviors(t, []behavior{
		{name: "update_order", src: updateOrderSource, stdout: updateOrderStdout},
		updateProperty(40),
	})
}

// updateProperty generates n cases over three record types: a base
// literal, one to three nested updates of random non-empty field sets, and
// a line printing the update and the base, with the model's expectation.
func updateProperty(n int) behavior {
	seed := uint32(0x2f6b1d)
	rnd := func(k int) int {
		seed = seed*1103515245 + 12345
		return int(seed>>8) % k
	}
	type rec struct {
		name   string
		fields []string // "int" or "string"
	}
	recs := []rec{{"A", []string{"int"}}, {"B", []string{"int", "string", "int"}}, {"C", []string{"string", "int", "string", "int", "int"}}}
	value := func(kind string) (src, out string) {
		if kind == "int" {
			v := strconv.Itoa(rnd(100) - 30)
			return v, v
		}
		v := "s" + strconv.Itoa(rnd(500))
		return strconv.Quote(v), v
	}
	var src, out strings.Builder
	for _, r := range recs {
		var fs, shown []string
		for i, k := range r.fields {
			fs = append(fs, "F"+strconv.Itoa(i)+" "+k)
			shown = append(shown, `\(x.F`+strconv.Itoa(i)+`)`)
		}
		src.WriteString("type " + r.name + " { " + strings.Join(fs, ", ") + " }\n\n")
		src.WriteString("func show" + r.name + "(x " + r.name + ") string { \"" + strings.Join(shown, " ") + "\" }\n\n")
	}
	src.WriteString("func main() needs Console {\n")
	for i := range n {
		r := recs[rnd(len(recs))]
		cur := make([]string, len(r.fields))
		var keyed []string
		for j, k := range r.fields {
			s, o := value(k)
			keyed = append(keyed, "F"+strconv.Itoa(j)+": "+s)
			cur[j] = o
		}
		base := append([]string(nil), cur...)
		b := "b" + strconv.Itoa(i)
		fmt.Fprintf(&src, "\tlet %s = %s{%s}\n", b, r.name, strings.Join(keyed, ", "))
		expr := b
		for range 1 + rnd(3) {
			var sets []string
			for j, k := range r.fields {
				if rnd(2) == 0 || j == len(r.fields)-1 && len(sets) == 0 {
					s, o := value(k)
					sets = append(sets, "F"+strconv.Itoa(j)+": "+s)
					cur[j] = o
				}
			}
			expr = r.name + "{.." + expr + ", " + strings.Join(sets, ", ") + "}"
		}
		fmt.Fprintf(&src, "\tlet u%d = %s\n", i, expr)
		fmt.Fprintf(&src, "\tConsole.Println(show%s(u%d) + \" | \" + show%s(%s))\n", r.name, i, r.name, b)
		out.WriteString(strings.Join(cur, " ") + " | " + strings.Join(base, " ") + "\n")
	}
	src.WriteString("}\n")
	return behavior{name: "update_property", src: src.String(), stdout: out.String()}
}

// goFuncBody returns the text of the emitted Go function name, from its
// func line to its closing brace.
func goFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	// A function whose first parameter is a local type is a Go method (196m).
	loc := regexp.MustCompile(`(?m)^func (\([^)]*\) )?` + regexp.QuoteMeta(name) + `\(`).FindStringIndex(src)
	if loc == nil {
		t.Fatalf("no emitted function %s in:\n%s", name, firstLines(src, 80))
	}
	rest := src[loc[0]:]
	// The closing brace may carry a /*line*/ directive.
	end := regexp.MustCompile(`(?m)^(/\*line [^*]*\*/)?}$`).FindStringIndex(rest)
	if end == nil {
		t.Fatalf("no end of the emitted function %s", name)
	}
	return rest[:end[1]]
}

// goTestEmittedVerbose runs the Go test text beside prog's emitted code, as
// goTestEmitted does, with -v, and returns its output.
func goTestEmittedVerbose(t *testing.T, prog *lowered, text string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("p/update/main.go", []byte(prog.mainGo()))
	write("p/update/update_test.go", []byte(text))
	cmd := exec.Command("go", "test", "-count=1", "-v", "./p/update")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("the emitted module's Go test failed: %v\n%s", err, firstLines(out.String(), 60))
	}
	return out.String()
}
