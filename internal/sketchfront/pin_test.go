package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/gobind"
)

// pinVersion is a generated ergo version: a tag vM.m.p, the pseudo-version
// of a commit with no tag below it (v0.0.0-TS-HASH), or the pseudo-version
// of a commit after the tag vM.m.p (vM.m.(p+1)-0.TS-HASH); a binary's may
// carry +dirty.
type pinVersion struct {
	major, minor, patch int
	pseudo              bool
	ts, hash            string
	dirty               bool
}

func (v pinVersion) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch)
	switch {
	case v.pseudo && v.major == 0 && v.minor == 0 && v.patch == 0:
		s += "-" + v.ts + "-" + v.hash
	case v.pseudo:
		s += "-0." + v.ts + "-" + v.hash
	}
	if v.dirty {
		s += "+dirty"
	}
	return s
}

// refOrder is the reference order of two generated versions, from their
// parts: the numbers, then a pseudo-version below the release of the same
// numbers, then two pseudo-versions by commit time, then by hash. Build
// metadata is no part of it.
func refOrder(a, b pinVersion) int {
	for _, d := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if d[0] != d[1] {
			return d[0] - d[1]
		}
	}
	switch {
	case a.pseudo != b.pseudo && a.pseudo:
		return -1
	case a.pseudo != b.pseudo:
		return 1
	case !a.pseudo:
		return 0
	}
	return strings.Compare(a.ts+a.hash, b.ts+b.hash)
}

func genPinVersion(r *rand.Rand, binary bool) pinVersion {
	tss := []string{"20261117152120", "20261117152121", "20261203000000"}
	hashes := []string{"0a1b2c3d4e5f", "c501620891c0"}
	v := pinVersion{major: r.IntN(2), minor: []int{0, 1, 2, 10}[r.IntN(4)], patch: r.IntN(2)}
	switch r.IntN(3) {
	case 1:
		v = pinVersion{pseudo: true}
	case 2:
		v.pseudo, v.patch = true, v.patch+1
	}
	if v.pseudo {
		v.ts, v.hash = tss[r.IntN(len(tss))], hashes[r.IntN(len(hashes))]
	}
	v.dirty = binary && r.IntN(4) == 0
	return v
}

// TestErgoLineIsAMinimum is SPLIT E1's acceptance (grammar.md, Source files
// 1, Which ergo): for generated pairs of a binary's version and an ergo.mod
// line's version (tags, both forms of pseudo-version, +dirty binaries and
// devel binaries), the loader reads the module exactly when the binary is
// at least the line by the reference order, a devel binary reads every
// module and says so, and a refusal names both versions and the install
// command. A failing seed prints both versions and the replay command.
//
// Mutants it alone catches (testdata/mutants.json): pin-compare-flipped,
// pin-devel-refuses.
func TestErgoLineIsAMinimum(t *testing.T) {
	dir := t.TempDir()
	saved, savedNotice := binaryVersion, develNotice
	t.Cleanup(func() { binaryVersion, develNotice = saved, savedNotice })
	notices := 0
	develNotice = func() { notices++ }
	refused, accepted := 0, 0
	for seed := uint64(1); seed <= 400; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x70696e))
		line := genPinVersion(r, false)
		bin := genPinVersion(r, true)
		devel := r.IntN(6) == 0
		binary := bin.String()
		if devel {
			binary = ergoversion.Devel
		}
		binaryVersion = func() string { return binary }
		if err := os.WriteFile(filepath.Join(dir, ModFile), []byte("module app\nergo "+line.String()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		before := notices
		_, _, err := FindModule(dir)
		want := devel || refOrder(bin, line) >= 0
		replay := fmt.Sprintf("seed %d: binary %s, line %s", seed, binary, line)
		switch {
		case want && err != nil:
			t.Fatalf("%s: refused, want read: %v\n--- replay: go test -run 'TestErgoLineIsAMinimum$' ./internal/sketchfront", replay, err)
		case !want && err == nil:
			t.Fatalf("%s: read, want refused\n--- replay: go test -run 'TestErgoLineIsAMinimum$' ./internal/sketchfront", replay)
		case !want && !strings.Contains(err.Error(), "needs ergo "+line.String()+" or newer, and this ergo is "+binary+"; install it: "+ergoversion.InstallCommand(line.String())):
			t.Fatalf("%s: the refusal does not name both versions and the install command: %v", replay, err)
		case devel && notices == before:
			t.Fatalf("%s: a devel binary read the line and did not say so", replay)
		}
		if want {
			accepted++
		} else {
			refused++
		}
	}
	if refused == 0 || accepted == 0 {
		t.Fatalf("the generator gave %d refusals and %d reads; both must occur", refused, accepted)
	}
}

// TestErgoLineRows (SPLIT E1): the refusal's position and the ergo line's
// own diagnostics, and the line of a required module, which counts as the
// highest line of the build list does. Mutant it alone catches
// (testdata/mutants.json): pin-dependency-line-ignored.
func TestErgoLineRows(t *testing.T) {
	saved := binaryVersion
	t.Cleanup(func() { binaryVersion = saved })
	binaryVersion = func() string { return "v1.2.0" }
	platform := gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	write := func(t *testing.T, files map[string]string) string {
		dir := t.TempDir()
		for name, text := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	t.Run("root", func(t *testing.T) {
		dir := write(t, map[string]string{"app/ergo.mod": "module app\n\nergo  v1.10.0\n", "app/main.ergo": "func main() {\n}\n"})
		a, err := checkDirIn(hostFS{}, filepath.Join(dir, "app"), platform, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "ergo.mod S108 3:7 ergo.mod needs ergo v1.10.0 or newer, and this ergo is v1.2.0; install it: go install github.com/cevr/ergo/cmd/ergo@v1.10.0\n"
		if got := diagnosticLines(a); got != want {
			t.Fatalf("got\n%swant\n%s", got, want)
		}
	})
	t.Run("dependency", func(t *testing.T) {
		files := map[string]string{
			"app/ergo.mod":      "module app\nergo v1.0.0\n",
			"app/go.mod":        "module app\n\ngo 1.27\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ../dep\n",
			"app/cmd/main.ergo": "import example.com/dep/lib\n\nfunc main() {\n\tlet _ = lib.F()\n}\n",
			"dep/ergo.mod":      "module example.com/dep\nergo v1.3.0\n",
			"dep/go.mod":        "module example.com/dep\n\ngo 1.27\n",
			"dep/lib/lib.ergo":  "func F() int {\n\treturn 1\n}\n",
		}
		dir := write(t, files)
		a, err := checkDirIn(hostFS{}, filepath.Join(dir, "app", "cmd"), platform, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "example.com/dep@v1.0.0/ergo.mod S108 2:6 example.com/dep@v1.0.0/ergo.mod needs ergo v1.3.0 or newer, and this ergo is v1.2.0; install it: go install github.com/cevr/ergo/cmd/ergo@v1.3.0\n"
		if got := diagnosticLines(a); got != want {
			t.Fatalf("got\n%swant\n%s", got, want)
		}
		binaryVersion = func() string { return "v1.3.0" }
		a, err = checkDirIn(hostFS{}, filepath.Join(dir, "app", "cmd"), platform, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := diagnosticLines(a); got != "" {
			t.Fatalf("an ergo at the dependency's line refuses it:\n%s", got)
		}
	})
	for _, c := range []struct{ text, problem string }{
		{"module app\nergo v1.2\n", "the ergo line's v1.2 is not a version of ergo: it is not vMAJOR.MINOR.PATCH"},
		{"module app\nergo 1.2.0\n", "it does not start with v"},
		{"module app\nergo v1.2.0+dirty\n", "it carries build metadata"},
		{"module app\nergo devel\n", "is not a version of ergo"},
		{"module app\nergo v1.0.0\nergo v1.1.0\n", "names the ergo it needs twice (line 2 and line 3)"},
	} {
		if _, d := parseModule(c.text); d == nil || d.Code != "S108" || !strings.Contains(d.Message, c.problem) {
			t.Errorf("%q: diagnostic %+v, want S108 %q", c.text, d, c.problem)
		}
	}
}
