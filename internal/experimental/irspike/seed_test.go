package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// seedProgram writes GenerateSeed(seed) into dir and loads it.
func seedProgram(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateSeed(seed)
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
	}
	return u, text
}

// emitSeed lowers u by the IR path with the seeded capacity on or off
// (lower.SetSeedCap) into dir.
func emitSeed(t *testing.T, u lower.Unit, on bool, dir string) lower.IRReport {
	t.Helper()
	restore := lower.SetSeedCap(on)
	defer restore()
	rep, err := Emit(u, IR, dir)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// seedSites is a program with one literal that seeds (a's) and its
// look-alikes that do not: a let mut only reassigned, a let, a literal of
// two elements, an Append whose value is another name's.
const seedSites = `func main() needs Console {
	let mut a = []int{1}
	a = a.Append(2)
	let mut b = []int{1}
	b = []int{2}
	let c = []int{1}
	let mut d = []int{1, 2}
	d = d.Append(3)
	let mut e = []int{1}
	let f = e.Append(2)
	e = []int{3}
	Console.Println("\(len(a)) \(len(b)) \(len(c)) \(len(d)) \(len(e)) \(len(f))")
}
`

// TestSeedCapSites: the IR seeds the one literal of seedSites that starts
// a let mut the body grows in place, and none with the switch off. In
// process: the IR report counts them.
//
// Mutants it alone catches: seedcap-any-let (a literal seeds any let mut
// of a slice, grown in place or not), seedcap-never (no literal seeds;
// TestSeedCapAgrees, which only compares outputs, still holds).
func TestSeedCapSites(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(seedSites), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	on := emitSeed(t, u, true, filepath.Join(dir, "on")).Seed
	off := emitSeed(t, u, false, filepath.Join(dir, "off")).Seed
	if on != 1 || off != 0 {
		t.Errorf("seeded literals: %d with the switch on, %d off; want 1, then 0", on, off)
	}
}

// TestSeedCapAgrees is RU11's seeded property: each seed program prints
// the same with the seeded capacity on and off (the reference "exact":
// the literal as written), and some literal is seeded. A failing seed
// prints the Ergo source, both outcomes and its replay command.
//
// Mutant it alone catches: seedcap-element-twice (the seeded slice holds
// its element twice: TestSeedCapSites only counts sites).
//
// It builds Go because the claim is about what the seeded slices do when
// the program runs, which only the compiled program shows.
func TestSeedCapAgrees(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_SEEDCAP_SEED", 1), envInt("ERGO_SEEDCAP_SEEDS", 8)
	seeded := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := seedProgram(t, seed, dir)
		seeded += emitSeed(t, u, true, filepath.Join(dir, "on")).Seed
		if n := emitSeed(t, u, false, filepath.Join(dir, "off")).Seed; n != 0 {
			t.Fatalf("seed %d: %d literals seeded under the reference", seed, n)
		}
		onBin, offBin := filepath.Join(dir, "on.bin"), filepath.Join(dir, "off.bin")
		for _, b := range [][2]string{{"on", onBin}, {"off", offBin}} {
			if _, err := Build(ir, IR, filepath.Join(dir, b[0]), b[1], BuildOptions{}); err != nil {
				t.Fatalf("seed %d: the %s build: %v\n%s", seed, b[0], err, text)
			}
		}
		if a, b := run(t, onBin), run(t, offBin); a != b {
			t.Errorf("seed %d: the seeded capacity changes the program\non:  %+v\noff: %+v\nreplay: ERGO_SEEDCAP_SEED=%d ERGO_SEEDCAP_SEEDS=1 go test -run 'TestSeedCapAgrees' ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
	}
	if seeded == 0 {
		t.Errorf("no seed seeded a literal: the property compares two equal lowerings")
	}
}
