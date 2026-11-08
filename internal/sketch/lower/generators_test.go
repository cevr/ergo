package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// generatorProbes is a program of properties, each refuted once a
// generated value holds one class a codec treats apart. A wide generator
// refutes every one of them within 100 cases.
const generatorProbes = `import strings
import utf8

func noControl(s string) bool {
	for i in 0..len(s) {
		if s[i] < 32 {
			return false
		}
	}
	true
}

func noAstral(s string) bool {
	for i in 0..len(s) {
		if s[i] >= 240 && s[i] <= 244 {
			return false
		}
	}
	true
}

test NoQuote(s string) {
	assert !strings.Contains(s, "\"")
}

test NoBackslash(s string) {
	assert !strings.Contains(s, "\\")
}

test NoDel(s string) {
	assert !strings.Contains(s, "\x7f")
}

test NoInvalid(s string) {
	assert !strings.ContainsRune(s, rune(65533))
}

test NoControl(s string) {
	assert noControl(s)
}

test NoAstral(s string) {
	assert noAstral(s)
}

test Short(xs []int) {
	assert len(xs) < 20
}

test FewKeys(m Map[string, int]) {
	assert len(m) < 12
}

test NoMax(n int64) {
	assert n != 9223372036854775807
}

test NoInt8Bound(n int8) {
	assert n != -128 && n != 127
}

test NoUint8Wrap(n uint8) {
	assert n != 255
}

test SmallStrings(s string) {
	assert utf8.RuneCount(s) <= 5
}
`

// TestGeneratorsReachEveryClass is the acceptance check of T4
// (tl-generators, decision O7). For each of seeds 1 to 5, within 100
// cases a generated string holds a quote, a backslash, DEL, a byte that is
// not UTF-8, a control byte and an astral rune; a slice reaches 20 elements
// and a Map 12 keys (the size grows with the case: case i has size i/4); a
// string grows past 5 bytes. Over the five seeds the integer edges appear
// (the int64 maximum, the int8 bounds, 255 for uint8). Each FAIL line names
// the seed and the size, and the size is the case's number over four.
func TestGeneratorsReachEveryClass(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "generators", generatorProbes)}, true)
	if exes == nil {
		return
	}
	always := []string{"NoQuote", "NoBackslash", "NoDel", "NoInvalid", "NoControl", "NoAstral", "Short", "FewKeys", "SmallStrings"}
	edges := []string{"NoMax", "NoInt8Bound", "NoUint8Wrap"}
	line := regexp.MustCompile(`^FAIL (\w+) \(case (\d+) of 100, seed (\d+), size (\d+): `)
	edgeSeen := map[string]int{}
	for seed := 1; seed <= 5; seed++ {
		stdout, stderr, _ := runExeArgs(t, exes["generators"], "--seed", fmt.Sprint(seed))
		failed := map[string]bool{}
		for _, l := range lines(stdout) {
			m := line.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			var c, s, size int
			fmt.Sscan(m[2], &c)
			fmt.Sscan(m[3], &s)
			fmt.Sscan(m[4], &size)
			if s != seed || size != c/4 {
				t.Errorf("seed %d: %q names seed %d and size %d, want seed %d and size %d", seed, l, s, size, seed, c/4)
			}
			failed[m[1]] = true
		}
		for _, name := range always {
			if !failed[name] {
				t.Errorf("seed %d: %s held for 100 cases: no generated value reached its class\n%s\n%s", seed, name, stdout, firstLines(stderr, 4))
			}
		}
		for _, name := range edges {
			if failed[name] {
				edgeSeen[name]++
			}
		}
	}
	for _, name := range edges {
		if edgeSeen[name] < 2 {
			t.Errorf("%s was refuted under %d of 5 seeds, want at least 2: the integer edges are too rare", name, edgeSeen[name])
		}
	}
}

// TestGeneratorSizeBoundsNesting: nested collections stay small, the bound
// halving at each level (at most 25 × 12 × 6 ints in a [][][]int); and
// case 1 (size 0) draws empty collections.
func TestGeneratorSizeBoundsNesting(t *testing.T) {
	src := `test Nested(xs [][][]int) {
	let mut n = 0
	for a in xs {
		for b in a {
			n = n + len(b)
		}
	}
	assert n <= 1800
}

test FirstIsEmpty(xs []int, m Map[int, int]) {
	assert len(xs) > 0 || len(m) > 0
}
`
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "nesting", src)}, true)
	if exes == nil {
		return
	}
	stdout, _, _ := runExe(t, exes["nesting"])
	got := lines(stdout)
	if len(got) != 3 || got[0] != "ok Nested (100 cases)" || !strings.HasPrefix(got[1], "FAIL FirstIsEmpty (case 1 of 100, seed 1, size 0: xs = []int{}, m = Map[int, int]{}): ") {
		t.Fatalf("stdout:\n%s", stdout)
	}
}
