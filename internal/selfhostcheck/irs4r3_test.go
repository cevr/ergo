package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestIRS4R3Cutover kills lower-s4r3-* mutants: a library package is
// lowered with the program that reaches it (lower.PackageInProgram,
// irdump's programOf), as the Go builder lowers it. Its unions take the
// program's flat form (a match and an injection in the fixture's
// producer, a match in std's semver and decimal under std's own program)
// and its calls the program's rewrite scope (producer.Scale), where they
// stopped before at "unproved union layout" and "rewrite scope not known".
//
// It builds compiler/cmd/irdump and runs it because the Ergo side is a
// whole compiled program, as TestCompilerIRAgreesWithBuilder's is.
func TestIRS4R3Cutover(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	fixture := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "bind", "port_s4r3")
	readerDir, producerDir := filepath.Join(fixture, "reader"), filepath.Join(fixture, "producer")
	u, err := irspike.Load(readerDir)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	line := func(name, dir string, pkg *check.Package) string {
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		e, f := filepath.Join(tmp, name+"-ext"), filepath.Join(tmp, name+"-foreign")
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		return strings.Join(append([]string{importPath(dir, pkg), e, f}, ergoFiles(t, dir)...), " ")
	}
	var producerPkg *check.Package
	for _, imp := range u.Imports {
		if imp.Package != nil && len(imp.Sources) > 0 && filepath.Dir(imp.Sources[0].Path) == producerDir {
			producerPkg = imp.Package
		}
	}
	if producerPkg == nil {
		t.Fatal("reader's unit holds no producer")
	}
	bases := []string{stdDir()}
	for _, p := range []string{"option", "collections", "core", "bits", "strings", "utf8", "semver", "decimal"} {
		bases = append(bases, filepath.Join(stdDir(), p))
	}
	lines := make([]string, len(bases))
	eachGoPackage(bases, func(i int, pkg *check.Package) {
		lines[i] = line(fmt.Sprintf("base%d", i), bases[i], pkg)
	})
	for i, l := range lines {
		if l == "" {
			t.Fatalf("std %s did not check", bases[i])
		}
	}
	readerPath := importPath(readerDir, u.Package)
	manifest := filepath.Join(tmp, "manifest")
	writeLines(t, manifest, append(lines, line("producer", producerDir, producerPkg), line("reader", readerDir, u.Package)))
	roots, tests := filepath.Join(tmp, "roots"), filepath.Join(tmp, "tests")
	writeLines(t, roots, []string{readerPath})
	writeLines(t, tests, []string{readerPath})

	paths := irPathIDs{}
	goIdx := irGoIndex{}
	if err := indexGoIR(t, paths, goIdx, irGoProgram{u: u, tests: true, dir: readerDir, module: fixture, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}
	var stdReach []*check.Func
	for _, spec := range []struct{ pkg, fn string }{
		{"semver", "isX"},
		{"semver", "num"},
		{"decimal", "bumps"},
	} {
		p, err := check.Std.Import(spec.pkg)
		if err != nil {
			t.Fatal(err)
		}
		var found *check.Func
		for _, fn := range p.Funcs {
			if fn.Name == spec.fn {
				found = fn
			}
		}
		if found == nil {
			t.Fatalf("std %s.%s not found", spec.pkg, spec.fn)
		}
		stdReach = append(stdReach, found)
	}
	const stdProgramDir = "/ergo-std-program"
	su, err := sketchfront.IRSpikeUnit(sketchfront.Check("func main() {}\n"), filepath.Join(stdProgramDir, "std.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	su.Root = stdProgramDir
	if err := indexGoIR(t, paths, goIdx, irGoProgram{u: su, module: stdProgramDir, reach: stdReach}); err != nil {
		t.Fatal(err)
	}

	type emission struct {
		key  irKey
		path string
		data []byte
	}
	emitted := map[string]emission{}
	var files, stops []string
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-tests", tests, "-corpus", manifest) {
		kind, rest, _ := strings.Cut(l, " ")
		switch kind {
		case "@pkg":
			files = nil
		case "P":
			_, path, _ := strings.Cut(rest, " ")
			files = append(files, path)
		case "S":
			stops = append(stops, l)
		case "F":
			f := strings.SplitN(rest, " ", 3)
			var fn irFunc
			if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
				t.Fatalf("unmarshal IR: %v", err)
			}
			if err := irMapFiles(&fn, files, paths); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(&fn)
			if err != nil {
				t.Fatal(err)
			}
			emitted[fn.Name] = emission{irKey{fn.Pos[0], fn.Pos[1], fn.Pos[2]}, f[0], got}
		}
	}
	for _, want := range []string{
		"PortS4r3Area",
		"ports4r3demo_sproducer__Area",
		"ports4r3demo_sproducer__MakeSquare",
		"ports4r3demo_sproducer__Scale",
		"ports4r3demo_sproducer__Double",
		"semver__isX",
		"semver__num",
		"decimal__bumps",
	} {
		got, ok := emitted[want]
		if !ok {
			t.Fatalf("s4r3 build did not emit %s (stops: %v)", want, relevantStops(stops))
		}
		res := goIdx.compare(got.key, irWitnessSite{got.path, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("s4r3 %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}
}

func relevantStops(stops []string) []string {
	var out []string
	for _, s := range stops {
		if strings.Contains(s, "port_s4r3") || strings.Contains(s, "/semver/") || strings.Contains(s, "/decimal/") {
			out = append(out, s)
		}
	}
	return out
}
