package linked

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// A stamp vouches for exactly the generation it records: Fresh is true for
// the files and inputs Stamp was given, and false once the inputs digest
// differs, any listed file's bytes differ, or a listed file is missing.
// Checked over seeded generations of 1 to 6 files. A false Fresh only
// costs TestLinkedGoIsFresh a regeneration; a wrong true would pass a
// stale file in the fast tier. Mutants it catches, which no other test
// does (TestLinkedGoIsFresh's repository is fresh, so it never sees a
// wrong true): Fresh not comparing the inputs digest
// (linked-fresh-inputs-ignored) or the files' hashes
// (linked-fresh-hash-ignored).
func TestStampVouchesOnlyForItsGeneration(t *testing.T) {
	r := rand.New(rand.NewPCG(20261119, 7))
	for seed := range 500 {
		files := map[string][]byte{}
		for i := range r.IntN(6) + 1 {
			data := make([]byte, r.IntN(64))
			for j := range data {
				data[j] = byte(r.UintN(256))
			}
			files[fmt.Sprintf("internal/linked/p%d/f%d.go", r.IntN(3), i)] = data
		}
		inputs := fmt.Sprintf("%016x", r.Uint64())
		stamp := Stamp(inputs, files)
		disk := map[string][]byte{}
		for k, v := range files {
			disk[k] = append([]byte(nil), v...)
		}
		read := func(rel string) ([]byte, error) {
			if d, ok := disk[rel]; ok {
				return d, nil
			}
			return nil, errors.New("missing")
		}
		if !Fresh(stamp, inputs, read) {
			t.Fatalf("seed %d: a stamp does not vouch for its own generation", seed)
		}
		if Fresh(stamp, inputs+"0", read) {
			t.Fatalf("seed %d: a stamp vouches for other inputs", seed)
		}
		rel := Sorted(files)[r.IntN(len(files))]
		saved := disk[rel]
		disk[rel] = append(append([]byte(nil), saved...), byte(r.UintN(256)))
		if Fresh(stamp, inputs, read) {
			t.Fatalf("seed %d: a stamp vouches for an edited %s", seed, rel)
		}
		delete(disk, rel)
		if Fresh(stamp, inputs, read) {
			t.Fatalf("seed %d: a stamp vouches for a missing %s", seed, rel)
		}
		disk[rel] = saved
	}
}
