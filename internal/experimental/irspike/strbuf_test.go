package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// strBufProgram writes text into dir and loads it.
func strBufProgram(t *testing.T, text, dir string) lower.Unit {
	t.Helper()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("the program does not check: %v\n%s", err, text)
	}
	return u
}

// emitStrBuf lowers u by the IR path with the built string on or off
// (lower.SetStrBuf) into dir.
func emitStrBuf(t *testing.T, u lower.Unit, on bool, dir string) lower.IRReport {
	t.Helper()
	restore := lower.SetStrBuf(on)
	defer restore()
	rep, err := Emit(u, IR, dir)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// strBufSites has one string local that takes the built form (a's) and
// look-alikes that do not: one grown only at its let's own depth, a
// prepend, one a closure reads, one read in a part of its own
// concatenation, one never grown in a loop.
const strBufSites = `func main() needs Console {
	let mut a = ""
	let mut b = ""
	let mut c = ""
	let mut d = ""
	let mut e = ""
	let mut f = ""
	for i in 0..3 {
		a = a + "\(i)" + " "
		let mut g = "g"
		g = g + "\(i)"
		Console.Println(g)
		b = "\(i)" + b
		c = c + "c"
		d = d + d
		e = "\(i)"
	}
	f = f + "f"
	let h = func() string { c }
	Console.Println("\(a) \(b) \(c) \(h()) \(d) \(e) \(f)")
}
`

// TestStrBufSites: the IR builds the one string local of strBufSites that
// a loop grows by concatenation, and none with the switch off. In process:
// the IR report counts them.
//
// Mutants it alone catches: strbuf-never (no local takes the form;
// TestStrBufAgrees, which compares outputs, still holds), strbuf-any-depth
// (a local grown only at its let's own depth takes it),
// strbuf-closure-read (a local a closure reads takes it).
func TestStrBufSites(t *testing.T) {
	dir := t.TempDir()
	u := strBufProgram(t, strBufSites, dir)
	on := emitStrBuf(t, u, true, filepath.Join(dir, "on")).StrBuf
	off := emitStrBuf(t, u, false, filepath.Join(dir, "off")).StrBuf
	if on != 1 || off != 0 {
		t.Errorf("built string locals: %d with the switch on, %d off; want 1, then 0", on, off)
	}
}

// TestStrBufAgrees is RU12's seeded property: each GenerateStrBuf program
// prints the same, stops where it stops and exits the same with the built
// string on and off (the reference: Go's concatenation), and some local
// takes the form. A failing seed prints the Ergo source, both outcomes and
// its replay command.
//
// Mutants it alone catches: strbuf-set-keeps-buffer (StrSet leaves inB
// set, so the next StrCat appends to the stale bytes), strbuf-cat-drops-set
// (the first StrCat after a StrSet does not copy the set text into the
// buffer), strbuf-get-stale (StrGet makes a string only once, so a read
// after a later StrCat returns the old text).
//
// It builds Go because the claim is about what the built strings hold
// when the program runs, which only the compiled program shows.
func TestStrBufAgrees(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_STRBUF_SEED", 1), envInt("ERGO_STRBUF_SEEDS", 8)
	built := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateStrBuf(seed)
		u := strBufProgram(t, text, dir)
		built += emitStrBuf(t, u, true, filepath.Join(dir, "on")).StrBuf
		emitStrBuf(t, u, false, filepath.Join(dir, "off"))
		onBin, offBin := filepath.Join(dir, "on.bin"), filepath.Join(dir, "off.bin")
		for _, b := range [][2]string{{"on", onBin}, {"off", offBin}} {
			if _, err := Build(ir, IR, filepath.Join(dir, b[0]), b[1], BuildOptions{}); err != nil {
				t.Fatalf("seed %d: the %s build: %v\n%s", seed, b[0], err, text)
			}
		}
		if a, b := run(t, onBin), run(t, offBin); a != b {
			t.Errorf("seed %d: the built string changes the program\non:  %+v\noff: %+v\nreplay: ERGO_STRBUF_SEED=%d ERGO_STRBUF_SEEDS=1 go test -run 'TestStrBufAgrees' ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
	}
	if built == 0 {
		t.Errorf("no seed built a string: the property compares two equal lowerings")
	}
}
