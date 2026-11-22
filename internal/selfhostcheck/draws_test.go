package selfhostcheck

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// TestUnusedDrawsAreKeyedByFile is a seeded property of unusedDraws: a
// pool that loses some files and gains one, anywhere in its order, keeps
// every remaining file's case, by name and byte for byte. The agreement
// ratchets compare named sites, so a corpus addition must add cases and
// change none (the unused seeds once drew pool[r.IntN(len(pool))], and
// five new std files moved the ratchet with no checker change). The pool
// is synthetic: the property is of the selection, not of the corpus.
func TestUnusedDrawsAreKeyedByFile(t *testing.T) {
	base := uint32(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = uint32(n)
	}
	file := func(path string, fns int) unusedFile {
		var b strings.Builder
		u := unusedFile{path: path}
		for i := range fns {
			fmt.Fprintf(&b, "func f%d() int {", i)
			u.bodies = append(u.bodies, b.Len())
			b.WriteString("\n\t1\n}\n\n")
		}
		u.src = b.String()
		return u
	}
	drawn := 0
	for seed := base; seed < base+200; seed++ {
		r := rand.New(rand.NewPCG(uint64(seed), 0x6b6579))
		var pool []unusedFile
		for i := range 20 + r.IntN(40) {
			pool = append(pool, file(fmt.Sprintf("examples/p%d/f%d.ergo", r.IntN(1000), i), 1+r.IntN(3)))
		}
		var after []unusedFile
		kept := map[string]bool{}
		for _, u := range pool {
			if r.IntN(5) > 0 {
				after = append(after, u)
				kept[u.path] = true
			}
		}
		at := r.IntN(len(after) + 1)
		added := file(fmt.Sprintf("internal/sketch/std/new%d/a.ergo", seed), 2)
		after = append(after[:at], append([]unusedFile{added}, after[at:]...)...)
		was := map[string]string{}
		for _, d := range unusedDraws(pool) {
			was[d.name] = d.src
		}
		now := map[string]string{}
		for _, d := range unusedDraws(after) {
			now[d.name] = d.src
		}
		replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestUnusedDrawsAreKeyedByFile$' ./internal/selfhostcheck (the first seed is the failing one)", seedVariable, seed)
		for name, src := range was {
			if !kept[strings.TrimPrefix(name, "unused ")] {
				continue
			}
			drawn++
			got, ok := now[name]
			if !ok {
				t.Fatalf("seed %d: %s is no longer drawn after the pool changed\n%s", seed, name, replay)
			}
			if got != src {
				t.Fatalf("seed %d: %s changed after the pool changed\n--- before\n%s\n--- after\n%s\n%s", seed, name, src, got, replay)
			}
		}
	}
	if drawn == 0 {
		t.Fatal("no kept file was drawn")
	}
}

// TestSourceInputsAreKeyedByName is the formatter inputs' form of the
// same property: sources that lose some members and gain one, anywhere in
// their order, keep every remaining source's derived inputs (its edit and
// perturbation) byte for byte. TestFormatAgreesWithSeed once drew every
// edit from one stream, so a file added to examples/ changed which inputs
// differed from the seed. The sources are synthetic.
func TestSourceInputsAreKeyedByName(t *testing.T) {
	base := uint64(1)
	if v := os.Getenv(seedVariable); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, v, err)
		}
		base = n
	}
	src := func(r *rand.Rand) string {
		return fmt.Sprintf("// c%d\nfunc f%d(a int) int {\n\tlet s = \"x%d\"\n\ta + %d\n}\n", r.IntN(100), r.IntN(100), r.IntN(100), r.IntN(100))
	}
	compared := 0
	for seed := base; seed < base+100; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x666d74))
		var sources []sketchtest.Input
		for i := range 10 + r.IntN(30) {
			sources = append(sources, sketchtest.Input{Name: fmt.Sprintf("examples/e%d/f%d.ergo", r.IntN(1000), i), Src: src(r)})
		}
		var after []sketchtest.Input
		kept := map[string]bool{}
		for _, in := range sources {
			if r.IntN(5) > 0 {
				after = append(after, in)
				kept[in.Name] = true
			}
		}
		at := r.IntN(len(after) + 1)
		added := sketchtest.Input{Name: fmt.Sprintf("examples/new%d/a.ergo", seed), Src: src(r)}
		after = append(after[:at], append([]sketchtest.Input{added}, after[at:]...)...)
		now := map[string]string{}
		for _, in := range derivedInputs(after, seed) {
			now[in.Name] = in.Src
		}
		replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestSourceInputsAreKeyedByName$' ./internal/selfhostcheck (the first seed is the failing one)", seedVariable, seed)
		for _, in := range derivedInputs(sources, seed) {
			if !kept[in.Name[:strings.LastIndex(in.Name, "/")]] && !strings.HasPrefix(in.Name, "random/") {
				continue
			}
			compared++
			got, ok := now[in.Name]
			if !ok || got != in.Src {
				t.Fatalf("seed %d: input %s changed after the sources changed\n--- before\n%q\n--- after\n%q\n%s", seed, in.Name, in.Src, got, replay)
			}
		}
	}
	if compared == 0 {
		t.Fatal("no derived input was compared")
	}
}
