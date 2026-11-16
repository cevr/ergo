package irspike

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/std"
)

// TestIRStdMatchesSource is G5's agreement property: for generated
// programs that call std functions (GenerateStd), the IR path and the
// source path print the same, exit the same and fail with the same
// message. Each of StdBodies, a library body of the program, gets an IR
// body, and that body starts at its own std file and line
// (std/<pkg>/<file>.ergo): the IR names the .ergo source of a std body as
// it does a program's (mutants ir-library-body-gated, ir-std-position-lost).
// A failing seed prints the Ergo source, both outcomes and its replay
// command. It builds Go for the reason needsBuild gives.
func TestIRStdMatchesSource(t *testing.T) {
	src, ir := needsBuild(t)
	want := stdBodyPositions(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateStd(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRStdMatchesSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		got := irBodyStarts(t, filepath.Join(dir, "ir", lower.IRFileName))
		var missing []string
		for _, w := range want {
			if !got[w.at] {
				missing = append(missing, w.name+" at "+w.at)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body starts at the std body's own line for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
	}
}

type stdBodyAt struct{ name, at string }

// stdBodyPositions are StdBodies' declarations as "file:line", the file
// spelled as a std body's location spells it (std/<pkg>/<file>.ergo).
func stdBodyPositions(t *testing.T) []stdBodyAt {
	t.Helper()
	var out []stdBodyAt
	for _, sb := range StdBodies {
		p, err := check.Std.Import(sb[0])
		if err != nil {
			t.Fatal(err)
		}
		var at string
		for _, f := range p.Funcs {
			if f.Name != sb[1] || f.Decl == nil {
				continue
			}
			for _, file := range p.Files() {
				for _, d := range file.Decls {
					if d != f.Decl {
						continue
					}
					line, _ := file.Position(f.Decl.Pos())
					path := "std/" + p.Path + "/" + file.Name
					if p.Path == std.PreludePath {
						path = "std/" + file.Name
					}
					at = fmt.Sprintf("%s:%d", path, line)
				}
			}
		}
		if at == "" {
			t.Fatalf("std %s.%s: no declaration", sb[0], sb[1])
		}
		out = append(out, stdBodyAt{sb[0] + "." + sb[1], at})
	}
	return out
}

// irBodyStarts are the "file:line" starts of the IR file's bodies.
func irBodyStarts(t *testing.T, irFile string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(irFile)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Files []string
		Funcs []struct{ Pos [3]int }
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, fn := range f.Funcs {
		if fn.Pos[0] < len(f.Files) {
			out[fmt.Sprintf("%s:%d", f.Files[fn.Pos[0]], fn.Pos[1])] = true
		}
	}
	return out
}
