package selfhostcheck

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerIncLawsRun runs the law of compiler/check/inc (capstone S3e,
// S3E-MODEL slice 2) through `ergo test`: over seeded edit sequences of a
// three-package program, the incremental check's rows equal the check
// from scratch, and its plan checks again exactly the rows the rule names
// (models/checkinc is the design, proved there over every sequence).
// Mutants of compiler/check/inc/testdata/mutants.json it alone catches:
// inc-drops-negative-read, inc-drops-local-read, inc-plan-ignores-fp,
// inc-lookup-ignores-export and inc-result-omits-ifp.
//
// It builds Go because `ergo test` lowers the law into a test program and
// runs it: the check-time evaluator skips the law (task.Check calls
// tasks.Transform, a Go function).
func TestCompilerIncLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/inc")
}

// TestIncPlanWorkWithinGoCache (S3E-MODEL S3) compares the work of
// compiler/check/inc's plan (cmd/incdump) with the Go checker's package
// cache (sketchfront.CheckCache) on the Go twin's program and edits
// (internal/sketchfront's TestIncrementalCheckAgrees): after each edit
// that leaves the program free of diagnostics, the packages with a row the
// plan checks again are no more than the packages the Go cache checks,
// and the plan's rows equal the rows from scratch. Work is counted in
// packages, the Go cache's unit (stage.PackagesChecked); the plan is finer,
// so it may check fewer. The comparison lasts while the Go checker does:
// it is deleted with it, and the Ergo law (TestCompilerIncLawsRun) stands
// alone. ERGO_SEED=<n> replays one seed.
//
// It builds Go because incdump is the Ergo program under test: the plan is
// Ergo source, and only its built form runs over the program's folders.
func TestIncPlanWorkWithinGoCache(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	dump := filepath.Join(t.TempDir(), "incdump")
	if out, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", dump, "./cmd/incdump"); err != nil {
		t.Fatalf("ergo build ./cmd/incdump: %v\n%s", err, out)
	}
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	fewer := 0
	for _, seed := range seeds {
		fewer += incWorkSeed(t, ctx, dump, seed)
	}
	if len(seeds) > 1 && fewer == 0 {
		t.Error("the plan never checked fewer packages than the Go cache: it is no finer than the package cache")
	}
}

// incWorkSeed runs one seed's edits and returns the steps where the plan
// checked fewer packages than the Go cache.
func incWorkSeed(t *testing.T, ctx context.Context, dump string, seed uint64) int {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 0x5e3e))
	root := t.TempDir()
	goDir := filepath.Join(root, "go")
	var p incTwin
	cache := sketchfront.NewCheckCache()
	var manifest strings.Builder
	var edits []string
	var goChecked []int64
	var clean []bool
	for step := range 7 {
		if step > 0 {
			e := r.IntN(incTwinFlags)
			p.flip(e)
			edits = append(edits, incTwinNames[e])
		}
		p.write(t, goDir)
		stepDir := filepath.Join(root, fmt.Sprintf("step%d", step))
		p.write(t, stepDir)
		manifest.WriteString("@step\n")
		for _, pkg := range [][2]string{{"inc/a", "a"}, {"inc/b", "b"}, {"inc/c", "c"}, {"inc/cmd/inc", "cmd/inc"}} {
			manifest.WriteString(pkg[0] + " " + filepath.Join(stepDir, filepath.FromSlash(pkg[1])) + "\n")
		}
		c0 := stage.Get(stage.PackagesChecked)
		a, err := cache.CheckDir(filepath.Join(goDir, "cmd", "inc"))
		if err != nil {
			t.Fatalf("seed %d after %v: %v", seed, edits, err)
		}
		goChecked = append(goChecked, stage.Get(stage.PackagesChecked)-c0)
		clean = append(clean, len(a.Diagnostics) == 0)
	}
	path := filepath.Join(root, "manifest")
	if err := os.WriteFile(path, []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCompiler(t, ctx, dump, path)
	if err != nil {
		t.Fatalf("incdump: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 7 {
		t.Fatalf("seed %d: incdump printed %d steps, want 7:\n%s", seed, len(lines), out)
	}
	fewer := 0
	for step, line := range lines {
		f := strings.Fields(line)
		if f[len(f)-1] != "agrees" {
			t.Errorf("seed %d after %v: step %d: the plan's rows differ from the rows from scratch: %s\nReplay: ERGO_SEED=%d", seed, edits[:step], step, line, seed)
		}
		if step == 0 || !clean[step] || !clean[step-1] {
			continue
		}
		n := int64(len(f) - 2)
		if n > goChecked[step] {
			t.Errorf("seed %d after %v: the plan checked %d packages (%s), the Go cache %d\nReplay: ERGO_SEED=%d", seed, edits[:step], n, line, goChecked[step], seed)
		}
		if n < goChecked[step] {
			fewer++
		}
	}
	return fewer
}

// incTwin is the Go twin's program (internal/sketchfront incProgram): a
// module of four packages, a, c, b (imports a) and the root cmd/inc
// (imports b and c, and a when mainA), written from edit flags.
type incTwin struct {
	comment, plus, rename, triple, extra, broken, quadPlus, cc, mainA bool
}

const incTwinFlags = 9

var incTwinNames = []string{"comment in a", "body of a.Double", "a.Open's parameter name", "export a.Triple", "file a/extra.ergo", "a type error in a", "body of b.Quad", "body of c.Name", "main imports a"}

func (p *incTwin) flip(i int) {
	f := []*bool{&p.comment, &p.plus, &p.rename, &p.triple, &p.extra, &p.broken, &p.quadPlus, &p.cc, &p.mainA}
	*f[i] = !*f[i]
}

func (p incTwin) files() map[string]string {
	a := ""
	if p.comment {
		a += "// Package a doubles.\n"
	}
	if p.plus {
		a += "func Double(n int) int {\n\tn + n\n}\n"
	} else {
		a += "func Double(n int) int {\n\tn * 2\n}\n"
	}
	a += "\ntype Box { V int }\n\n"
	if p.rename {
		a += "func Open(box Box) int {\n\tbox.V\n}\n"
	} else {
		a += "func Open(b Box) int {\n\tb.V\n}\n"
	}
	if p.triple {
		a += "\nfunc Triple(n int) int {\n\tn * 3\n}\n"
	}
	if p.broken {
		a += "\nfunc bad() int {\n\t\"x\"\n}\n"
	}
	b := "import inc/a\n\n"
	if p.quadPlus {
		b += "func Quad(n int) int {\n\ta.Double(n) + a.Double(n)\n}\n"
	} else {
		b += "func Quad(n int) int {\n\ta.Double(a.Double(n))\n}\n"
	}
	b += "\nfunc Unbox(n int) int {\n\ta.Open(a.Box{V: n})\n}\n\ntest QuadIsFour {\n\tassert Quad(1) == 4\n\tassert Unbox(3) == 3\n}\n"
	c := "func Name() string {\n\t\"c\"\n}\n"
	if p.cc {
		c = "func Name() string {\n\t\"c\" + \"c\"\n}\n"
	}
	m := "import inc/b\nimport inc/c\n"
	use := "b.Quad(2)"
	if p.mainA {
		m += "import inc/a\n"
		use = "a.Double(b.Quad(2))"
	}
	m += "\nfunc main() needs Console {\n\tConsole.Println(\"\\(" + use + ") \\(c.Name())\")\n}\n"
	out := map[string]string{"ergo.mod": "module inc\n", "a/a.ergo": a, "b/b.ergo": b, "c/c.ergo": c, "cmd/inc/main.ergo": m}
	if p.extra {
		out["a/extra.ergo"] = "func Extra() int {\n\t1\n}\n"
	}
	return out
}

// write makes dir hold exactly p's files.
func (p incTwin) write(t *testing.T, dir string) {
	t.Helper()
	files := p.files()
	if files["a/extra.ergo"] == "" {
		if err := os.Remove(filepath.Join(dir, "a", "extra.ergo")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for rel, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
