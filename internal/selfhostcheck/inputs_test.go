package selfhostcheck

import (
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// seedVariable selects the seed of the generated inputs; the default is
// deterministic.
const seedVariable = "ERGO_SELFHOST_SEED"

// sourceInputs are the repository's sources and their edits: every
// examples/**/*.ergo and compiler/ .ergo file (an invalid one is compared
// as a failure), sketchtest.Positive(), a whitespace perturbation of each,
// and seeded edits: truncations, invalid UTF-8 inside comments and
// strings, random bytes, and random byte strings.
func sourceInputs(t *testing.T, seed uint64) []sketchtest.Input {
	t.Helper()
	var inputs []sketchtest.Input
	root := repoRoot()
	for _, dir := range []string{filepath.Join(root, "examples"), filepath.Join(root, "compiler")} {
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

// repoRoot is the repository root, found from this file's location.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// ergoAnswer is the linked package's answer, or its Go error as text.
func ergoAnswer(s string, err error) string {
	if err != nil {
		return "ergo error " + err.Error() + "\n"
	}
	return s
}
