package genmark

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestMarkerHoldsOnlyItsFolder writes seeded trees of files, stamps each
// and holds Held to the model: a stamped folder is held; one byte changed
// in any file, a file added or a file removed, and the marker no longer
// holds; the marker copied into another folder of other files does not
// hold there; With's marker over the same files is Stamp's, and Parse
// reads back what Text wrote. Mutants: genmark-held-ignores-digest,
// genmark-digest-skips-content, genmark-digest-skips-names.
func TestMarkerHoldsOnlyItsFolder(t *testing.T) {
	for seed := range uint64(40) {
		r := rand.New(rand.NewPCG(seed, 0x9e))
		files := map[string]string{}
		for i := range 1 + r.IntN(5) {
			name := fmt.Sprintf("f%d.ergo", i)
			if r.IntN(3) == 0 {
				name = fmt.Sprintf("d%d/%s", r.IntN(2), name)
			}
			files[name] = fmt.Sprintf("let x%d = %d\n", i, r.IntN(100))
		}
		dir := t.TempDir()
		write := func(dir string, files map[string]string) {
			t.Helper()
			for name, text := range files {
				p := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		write(dir, files)
		mark := Marker{Kind: "cli", Generator: "ergo gen cli", Subjects: []string{"m/p.Op"}, Contract: "sha256:00"}
		if err := Stamp(dir, mark); err != nil {
			t.Fatal(err)
		}
		if !Held(dir) {
			t.Fatalf("seed %d: a folder just stamped is not held", seed)
		}
		stamped, err := os.ReadFile(filepath.Join(dir, Name))
		if err != nil {
			t.Fatal(err)
		}
		if got := With(files, mark)[Name]; got != string(stamped) {
			t.Fatalf("seed %d: With's marker\n%s\nis not Stamp's\n%s", seed, got, stamped)
		}
		if m, err := Parse(string(stamped)); err != nil || m.Text() != string(stamped) {
			t.Fatalf("seed %d: Parse(Text) = %+v, %v", seed, m, err)
		}

		other := t.TempDir()
		moved := map[string]string{}
		for name, text := range files {
			moved[name+"x"] = text
		}
		write(other, moved)
		write(other, map[string]string{Name: string(stamped)})
		if Held(other) {
			t.Fatalf("seed %d: the marker holds in a folder of other files", seed)
		}

		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		slices.Sort(names)
		name := names[r.IntN(len(names))]
		victim := filepath.Join(dir, filepath.FromSlash(name))
		edited := []byte(files[name])
		edited[r.IntN(len(edited)-1)] ^= 1
		write(dir, map[string]string{name: string(edited)})
		if Held(dir) {
			t.Fatalf("seed %d: the marker holds after %s was edited", seed, victim)
		}
		write(dir, map[string]string{name: files[name]})
		extra := filepath.Join(dir, "added.ergo")
		write(dir, map[string]string{"added.ergo": "let y = 1\n"})
		if Held(dir) {
			t.Fatalf("seed %d: the marker holds after a file was added", seed)
		}
		if err := os.Remove(extra); err != nil {
			t.Fatal(err)
		}
		if !Held(dir) {
			t.Fatalf("seed %d: restored, the folder is not held", seed)
		}
		if err := os.Remove(victim); err != nil {
			t.Fatal(err)
		}
		if Held(dir) {
			t.Fatalf("seed %d: the marker holds after a file was removed", seed)
		}
	}
}
