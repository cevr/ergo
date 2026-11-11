package selfhostcheck

import (
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// seedVariable selects the seed of the generated inputs; the default is
// deterministic.
const seedVariable = "ERGO_SELFHOST_SEED"

// TestSelfhostBridgeAgrees is SH0's acceptance: the Go parser's tree
// crosses into Ergo and back intact. For every input, the Go adapter
// (hostsyntax) writes the parser's facts as a wire, the Ergo package tree
// decodes it into its arena, and the Ergo library formatapi writes the
// projection of what it decoded; the driver writes the same projection
// straight from syntax.Parse's tree, by reflection. The two must be equal
// byte for byte: every node, field, string byte, list item, position (with
// its line and column), absent position and syntax failure.
//
// Inputs: every examples/**/*.ergo (an invalid one is compared as a
// failure), sketchtest.Positive(), a whitespace perturbation of each, and
// seeded edits: truncations, invalid UTF-8 inside comments and strings,
// random bytes, and random byte strings.
//
// Mutants it alone catches (testdata/mutants.json): sh-tree-drops-child,
// sh-tree-loses-absent-pos, sh-wire-replaces-invalid-byte,
// sh-tree-column-off-by-one, sh-wire-drops-error-location.
//
// It builds Go because the property executes the Ergo decoder through its
// exported Go ABI: only a built library shows what crosses the boundary.
func TestSelfhostBridgeAgrees(t *testing.T) {
	needsBuild(t)
	seed := uint64(1)
	if s := os.Getenv(seedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, s, err)
		}
		seed = n
	}
	inputs := bridgeInputs(t, seed)
	if len(inputs) == 0 {
		t.Fatal("no inputs")
	}
	exe := buildDriver(t, "formatapi")
	srcs := make([]string, len(inputs))
	for i, in := range inputs {
		srcs[i] = in.Src
	}
	answers := runDriver(t, exe, srcs)
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestSelfhostBridgeAgrees$' ./internal/selfhostcheck"
	failed := 0
	for i, a := range answers {
		if a.goSide == a.ergoSide {
			continue
		}
		failed++
		if failed > 3 {
			continue
		}
		line, g, e := firstDiff(a.goSide, a.ergoSide)
		t.Errorf("seed %d: input %s: the projections differ at line %d\n  go:   %q\n  ergo: %q\ninput: %q\n--- go\n%s--- ergo\n%s%s",
			seed, inputs[i].Name, line, g, e, inputs[i].Src, clip(a.goSide), clip(a.ergoSide), replay)
	}
	if failed > 0 {
		t.Errorf("%d of %d inputs differ", failed, len(answers))
	}
}

// bridgeInputs is the input domain of the property.
func bridgeInputs(t *testing.T, seed uint64) []sketchtest.Input {
	t.Helper()
	var inputs []sketchtest.Input
	root := repoRoot()
	for _, dir := range []string{filepath.Join(root, "examples"), pilot()} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() && strings.HasSuffix(path, ".ergo") {
				src, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(root, path)
				inputs = append(inputs, sketchtest.Input{Name: filepath.ToSlash(rel), Src: string(src)})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	positive, err := sketchtest.Positive()
	if err != nil {
		t.Fatal(err)
	}
	inputs = append(inputs, positive...)
	r := rand.New(rand.NewPCG(seed, 0x5e1f))
	base := len(inputs)
	for i := range base {
		in := inputs[i]
		if p, ok := sketchtest.Perturb(in.Src, seed+uint64(i)); ok {
			inputs = append(inputs, sketchtest.Input{Name: in.Name + "/perturbed", Src: p})
		}
		inputs = append(inputs, sketchtest.Input{Name: in.Name + "/edited", Src: edit(r, in.Src)})
	}
	for i := range 64 {
		b := make([]byte, r.IntN(48))
		for j := range b {
			b[j] = byte(r.IntN(256))
		}
		inputs = append(inputs, sketchtest.Input{Name: "random/" + strconv.Itoa(i), Src: string(b)})
	}
	return inputs
}

// invalid are byte runs that are not UTF-8: a stray continuation byte, a
// truncated sequence, an overlong encoding, an encoded surrogate.
var invalid = []string{"\x80", "\xc3", "\xc0\xaf", "\xed\xa0\x80", "\xff"}

// edit is one seeded edit of src: invalid UTF-8 inside a comment or a
// string literal (so the source may still parse), a truncation, or a
// random byte.
func edit(r *rand.Rand, src string) string {
	bad := invalid[r.IntN(len(invalid))]
	switch r.IntN(4) {
	case 0:
		if i := strings.Index(src, "//"); i >= 0 {
			return src[:i+2] + bad + src[i+2:]
		}
		return "// " + bad + "\n" + src
	case 1:
		if i := strings.Index(src, "\""); i >= 0 {
			return src[:i+1] + bad + src[i+1:]
		}
		return src + "\nlet s = \"" + bad + "\"\n"
	case 2:
		return src[:r.IntN(len(src)+1)]
	default:
		i := r.IntN(len(src) + 1)
		return src[:i] + string(rune(r.IntN(128))) + src[i:]
	}
}

// firstDiff is the 1-based number of the first line where a and b differ,
// and the two lines.
func firstDiff(a, b string) (int, string, string) {
	al, bl := strings.SplitAfter(a, "\n"), strings.SplitAfter(b, "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return i + 1, x, y
		}
	}
	return 0, "", ""
}

// clip keeps a failure report readable.
func clip(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "\n…\n"
	}
	return s
}
