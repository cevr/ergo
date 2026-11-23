package main

import (
	"bytes"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The GO-DROPIN-PROBE module: Go packages over one Ergo package, lib.
const (
	goMixMod = "module example.com/mix\n\ngo 1.27\n"
	goMixLib = `// Add is a + b.
func Add(a int, b int) int { a + b }

// DivZero is a division by zero.
type DivZero { Of int }

// Div is a / b, or DivZero when b is 0.
func Div(a int, b int) int fails DivZero {
	if b == 0 {
		return DivZero{Of: a}
	}
	a / b
}

// Sum is the sum of xs.
func Sum(xs []int) int {
	let mut s = 0
	for x in xs {
		s += x
	}
	s
}
`
	// goMixHalf imports lib (ERGO-GO-IMPORTS): its failure is lib's
	// DivZero, and it embeds an asset of its own folder.
	goMixHalf = `import example.com/mix/lib

// Of is n / d through lib.Div.
func Of(n int, d int) int fails lib.DivZero { lib.Div(n, d) }

let note = embedded[string]("note.txt")

// Note is half's embedded note.
func Note() string { note }
`
	// goMixBoth imports lib and half: the diamond.
	goMixBoth = `import example.com/mix/half
import example.com/mix/lib

// Sum is n / d through half, plus n + d through lib.
func Sum(n int, d int) int fails lib.DivZero {
	let q = half.Of(n, d)
	q + lib.Add(n, d)
}
`
	goMixCalc = `// Package calc is Go over the Ergo package lib.
package calc

import (
	"context"

	"example.com/mix/lib"
)

func Half(ctx context.Context, n int) (int, error) { return lib.Div(ctx, n, 2) }

func Ratio(ctx context.Context, a, b int) (int, error) { return lib.Div(ctx, a, b) }

func Total(ctx context.Context, xs []int, n int) (int, error) {
	s, err := lib.Sum(ctx, xs)
	if err != nil {
		return 0, err
	}
	return lib.Add(ctx, s, n)
}
`
	goMixCalcTest = `package calc

import (
	"context"
	"errors"
	"testing"

	"example.com/mix/both"
	"example.com/mix/half"
	"example.com/mix/lib"
)

func TestHalf(t *testing.T) {
	if got, err := Half(context.Background(), 7); err != nil || got != 3 {
		t.Fatalf("Half(7) = %d, %v", got, err)
	}
}

func TestRatioByZero(t *testing.T) {
	_, err := Ratio(context.Background(), 5, 0)
	var dz lib.DivZero
	if !errors.As(err, &dz) || dz.Of != 5 {
		t.Fatalf("Ratio(5, 0) error = %v, want DivZero{Of: 5}", err)
	}
}

func TestTotal(t *testing.T) {
	if got, err := Total(context.Background(), []int{1, 2, 3}, 4); err != nil || got != 10 {
		t.Fatalf("Total = %d, %v", got, err)
	}
}

// TestThroughTheImports: lib's DivZero is one Go type, whether Go reaches
// it through half, through both (the diamond) or through lib.
func TestThroughTheImports(t *testing.T) {
	ctx := context.Background()
	_, err := half.Of(ctx, 7, 0)
	var viaHalf lib.DivZero
	if !errors.As(err, &viaHalf) || viaHalf.Of != 7 {
		t.Fatalf("half.Of(7, 0) error = %v, want lib.DivZero{Of: 7}", err)
	}
	_, err = both.Sum(ctx, 9, 0)
	var viaBoth lib.DivZero
	if !errors.As(err, &viaBoth) || viaBoth.Of != 9 {
		t.Fatalf("both.Sum(9, 0) error = %v, want lib.DivZero{Of: 9}", err)
	}
	_, err = lib.Div(ctx, 3, 0)
	var direct lib.DivZero
	if !errors.As(err, &direct) {
		t.Fatalf("lib.Div(3, 0) error = %v, want lib.DivZero", err)
	}
	direct, viaBoth = viaHalf, direct
	if direct.Of != 7 || viaBoth.Of != 3 {
		t.Fatalf("assigned across the paths: %v %v", direct, viaBoth)
	}
	if got, err := both.Sum(ctx, 8, 2); err != nil || got != 14 {
		t.Fatalf("both.Sum(8, 2) = %d, %v, want 14", got, err)
	}
	if got, err := half.Note(ctx); err != nil || got != "a note\n" {
		t.Fatalf("half.Note() = %q, %v, want the embedded note", got, err)
	}
}
`
	goMixApp = `package main

import (
	"context"
	"fmt"

	"example.com/mix/calc"
	"example.com/mix/lib"
)

func main() {
	ctx := context.Background()
	h, err := calc.Half(ctx, 9)
	fmt.Println("half", h, err)
	_, err = calc.Ratio(ctx, 1, 0)
	fmt.Println("ratio", err)
	a, err := lib.Add(ctx, 40, 2)
	fmt.Println("add", a, err)
}
`
)

// treeFiles are the module's files and their bytes, by relative path.
func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		out[rel] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestErgoGoBuildsAModuleWithAnErgoPackage is the acceptance of ERGO-GO and
// ERGO-GO-IMPORTS: in a Go module (no ergo.mod) whose packages lib, half
// (which imports lib) and both (which imports lib and half) are Ergo,
// `ergo go test ./...` runs calc's Go tests: an errors.As of lib's typed
// failure, reached through lib, through half and through both, and the
// values assigned across those paths (one Go type), and half's embedded
// asset. `ergo go run` runs the Go main, an edit of lib.ergo alone changes
// what the next run computes (the IR digest: GO-DROPIN-PROBE step 10), a
// type error in lib.ergo is reported at lib.ergo's line, and the module's
// own files are unchanged. It builds Go because the question is whether
// cmd/go, given ergo's -overlay, -gcflags=-ergoir and -modfile, builds,
// tests and runs the module: only the go command answers it. The smoke
// runner bounds every child.
//
// Mutants: gocmd-no-ergoir (Assemble passes no -ergoir), gocmd-no-overlay
// (the declaration file is not overlaid), gocmd-no-runtime (the go.mod
// copy does not require the runtime), sketchfront-golib-relative-lines
// (GoPackages keeps the module-relative names), gocmd-asset-by-folder
// (an asset is placed by its own folder, not its package's).
func TestErgoGoBuildsAModuleWithAnErgoPackage(t *testing.T) {
	if testing.Short() {
		t.Skip("ergo go builds and runs Go: the full tier")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	smokeWrite(t, root, "go.mod", goMixMod)
	smokeWrite(t, root, "lib/lib.ergo", goMixLib)
	smokeWrite(t, root, "half/half.ergo", goMixHalf)
	smokeWrite(t, root, "half/note.txt", "a note\n")
	smokeWrite(t, root, "both/both.ergo", goMixBoth)
	smokeWrite(t, root, "calc/calc.go", goMixCalc)
	smokeWrite(t, root, "calc/calc_test.go", goMixCalcTest)
	smokeWrite(t, root, "cmd/app/main.go", goMixApp)
	before := treeFiles(t, root)
	run := func(args ...string) (string, string, int) {
		t.Helper()
		out, err, code := runSmokeCLI(t, bin, root, "", append([]string{"go"}, args...)...)
		return string(out), string(err), code
	}
	if out, stderr, code := run("test", "-count=1", "./..."); code != 0 || !strings.Contains(out, "ok  \texample.com/mix/calc") {
		t.Fatalf("ergo go test ./...: exit %d\n%s\n%s", code, out, stderr)
	}
	want := "half 4 <nil>\nratio DivZero{Of:1}\nadd 42 <nil>\n"
	if out, stderr, code := run("run", "./cmd/app"); code != 0 || out != want {
		t.Fatalf("ergo go run ./cmd/app: exit %d\n%q, want %q\n%s", code, out, want, stderr)
	}
	if out, stderr, code := run("vet", "./..."); code != 0 {
		t.Fatalf("ergo go vet ./...: exit %d\n%s\n%s", code, out, stderr)
	}
	smokeWrite(t, root, "lib/lib.ergo", strings.Replace(goMixLib, "{ a + b }", "{ a - b }", 1))
	if out, stderr, code := run("run", "./cmd/app"); code != 0 || !strings.Contains(out, "add 38 <nil>") {
		t.Fatalf("after Add became a - b, ergo go run ./cmd/app: exit %d\n%q, want add 38\n%s", code, out, stderr)
	}
	smokeWrite(t, root, "lib/lib.ergo", strings.Replace(goMixLib, "{ a + b }", `{ a + "b" }`, 1))
	if out, stderr, code := run("build", "./..."); code != 1 || !strings.Contains(stderr, "lib.ergo:2:") {
		t.Fatalf("a type error at lib.ergo:2: ergo go build: exit %d, want 1 naming lib.ergo:2\n%s\n%s", code, out, stderr)
	}
	smokeWrite(t, root, "lib/lib.ergo", goMixLib)
	if after := treeFiles(t, root); !maps.Equal(before, after) {
		t.Fatalf("ergo go changed the module's files:\nbefore %v\nafter  %v", slices.Sorted(maps.Keys(before)), slices.Sorted(maps.Keys(after)))
	}
}

// TestErgoGoWithoutErgoIsThePinnedGo: in a module with no Ergo package,
// `ergo go build -o X` writes the bytes the pinned go command writes (the
// toolchain superset of GO-INTEROP A3). It builds Go because the bytes of
// a linked executable are the claim.
//
// Mutant: gocmd-always-patched (ergo go runs the patched go for a module
// with no Ergo package).
func TestErgoGoWithoutErgoIsThePinnedGo(t *testing.T) {
	if testing.Short() {
		t.Skip("ergo go builds Go: the full tier")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	smokeWrite(t, root, "go.mod", goMixMod)
	smokeWrite(t, root, "cmd/app/main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"plain Go\") }\n")
	ours, theirs := filepath.Join(t.TempDir(), "ours"), filepath.Join(t.TempDir(), "theirs")
	if out, stderr, code := runSmokeCLI(t, bin, root, "", "go", "build", "-o", ours, "./cmd/app"); code != 0 {
		t.Fatalf("ergo go build: exit %d\n%s\n%s", code, out, stderr)
	}
	selected, err := goCommand()
	if err != nil {
		t.Fatal(err)
	}
	stock := exec.Command(selected.Go, "build", "-o", theirs, "./cmd/app")
	stock.Dir = root
	if selected.Root != "" {
		stock.Env = append(os.Environ(), "GOROOT="+selected.Root, "GOTOOLCHAIN=local")
	}
	if out, stderr, code := runSmokeCommand(t, stock); code != 0 {
		t.Fatalf("go build: exit %d\n%s\n%s", code, out, stderr)
	}
	a, b := smokeRead(t, ours), smokeRead(t, theirs)
	if !bytes.Equal(a, b) {
		t.Fatalf("ergo go build wrote %d bytes, the pinned go %d: not the same executable", len(a), len(b))
	}
}
