package genprogram

import (
	"maps"
	"testing"
)

// TestGenerateProgramIsSeeded: a program is a function of its seed and
// size, and the seed matters: the same seed gives the same files, and
// seeds 1 to 4 give four programs. It kills cs-gen-not-seeded, which the
// counted tests cannot see: their reference counts read whatever files the
// generator wrote.
func TestGenerateProgramIsSeeded(t *testing.T) {
	seen := map[string]uint64{}
	for seed := uint64(1); seed <= 4; seed++ {
		p := GenerateProgram(seed, 12, Options{})
		if again := GenerateProgram(seed, 12, Options{}); !maps.Equal(p.Files, again.Files) {
			t.Fatalf("seed %d gives two programs", seed)
		}
		text := p.Files[Name(9)+"/"+Name(9)+".ergo"]
		if other, ok := seen[text]; ok {
			t.Fatalf("seeds %d and %d give the same package %s", other, seed, Name(9))
		}
		seen[text] = seed
	}
}
