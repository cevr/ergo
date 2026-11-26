package selfhostcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/testwait"
)

// TestIRLenMethod kills lower-len-method-receiver and
// lower-len-method-purity: a built-in .Len() call, whose checked callee is
// declared extern go len, lowers to the Go builder's len of its receiver,
// in a body and as a pure callee edge, and a Map receiver or a constant
// still stops. It builds Go because the self-hosted IR builder runs only
// as the irdump program that ergo builds from compiler/lower; no
// in-process form of it exists.
func TestIRLenMethod(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	const src = `func main() {
	let _ = TextLen("ab")
	let _ = SliceLen([]int{1, 2})
	let _ = SameLen([]int{1}, []int{2})
	let _ = Shorter("a", 2)
	let _ = TextLenPlus("ab")
	let _ = MapLenStop(Map[string, int]{})
	let _ = ConstLenStop()
}

func TextLen(s string) int {
	s.Len()
}

func SliceLen(xs []int) int {
	xs.Len()
}

func SameLen(a []int, b []int) bool {
	a.Len() == b.Len()
}

func Shorter(s string, n int) bool {
	s.Len() < n
}

func TextLenPlus(s string) int {
	TextLen(s) + 1
}

func MapLenStop(m Map[string, int]) int {
	m.Len()
}

func ConstLenStop() int {
	"abc".Len()
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := irspike.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ext := goKinds(u.Package)
	_, foreign := goTypes(u.Package)
	pkgPath := importPath(dir, u.Package)
	tmp := t.TempDir()
	ef, ff := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	writeLines(t, ef, ext)
	writeLines(t, ff, foreign)
	bases := []string{stdDir(), filepath.Join(stdDir(), "option"), filepath.Join(stdDir(), "collections"), filepath.Join(stdDir(), "core")}
	lines := make([]string, len(bases))
	eachGoPackage(bases, func(i int, pkg *check.Package) {
		_, bext := goKinds(pkg)
		_, bforeign := goTypes(pkg)
		be, bf := filepath.Join(tmp, fmt.Sprintf("base-ext%d", i)), filepath.Join(tmp, fmt.Sprintf("base-foreign%d", i))
		writeLines(t, be, bext)
		writeLines(t, bf, bforeign)
		lines[i] = strings.Join(append([]string{importPath(bases[i], pkg), be, bf}, ergoFiles(t, bases[i])...), " ")
	})
	for i, l := range lines {
		if l == "" {
			t.Fatalf("std %s did not check", bases[i])
		}
	}
	manifest := filepath.Join(tmp, "manifest")
	writeLines(t, manifest, append(lines, strings.Join([]string{pkgPath, ef, ff, file}, " ")))
	roots := filepath.Join(tmp, "roots")
	writeLines(t, roots, []string{pkgPath})

	paths := irPathIDs{}
	goIdx := irGoIndex{}
	if err := indexGoIR(t, paths, goIdx, irGoProgram{u: u, tests: false, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}

	emitted := map[string]struct {
		key  irKey
		data []byte
	}{}
	var files, stops []string
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-corpus", manifest) {
		kind, rest, _ := strings.Cut(l, " ")
		switch kind {
		case "@pkg":
			files = nil
		case "P":
			_, path, _ := strings.Cut(rest, " ")
			files = append(files, path)
		case "S":
			if strings.Contains(l, file) {
				stops = append(stops, l)
			}
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
			emitted[fn.Name] = struct {
				key  irKey
				data []byte
			}{irKey{fn.Pos[0], fn.Pos[1], fn.Pos[2]}, got}
		}
	}

	for _, want := range []string{"TextLen", "SliceLen", "SameLen", "Shorter", "TextLenPlus"} {
		got, ok := emitted[want]
		if !ok {
			t.Fatalf("len-method build did not emit %s (stops: %v)", want, stops)
		}
		res := goIdx.compare(got.key, irWitnessSite{file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("len-method %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}
	for _, forbidden := range []string{"MapLenStop", "ConstLenStop"} {
		if _, ok := emitted[forbidden]; ok {
			t.Fatalf("unadmitted len method %s must stop honestly and not emit", forbidden)
		}
	}
}
