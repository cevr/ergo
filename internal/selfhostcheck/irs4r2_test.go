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
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestIRS4R2Cutover kills lower-s4r2-* mutants: import go leaf pcalls
// (both unquoted and quoted paths), runtime/v1 leaf extern pcalls, seeded
// mut slice literals with Append loop rewriting, read-only package-level
// slice globals, typed unary negative constant folds, generic unit result
// admission, prelude collision suffixes, and sealed union admission, while
// refusing unadmitted non-basic mut slices and globals honestly.
func TestIRS4R2Cutover(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	const src = `import go math
import go unicode/utf8
import strings

type SealedRes = SealedOk{Val int, Pad [20]int} | SealedZero{}

type S4r2Item { K int, V int }

type BadBag { Xs []int }

let s4r2Items = []S4r2Item{S4r2Item{K: 1, V: 10}, S4r2Item{K: 2, V: 20}}

let s4r2Bags = []BadBag{BadBag{Xs: []int{1}}}

func main() {
	let _ = UseSealed(SealedZero)
	let _ = MatchSealed(SealedZero)
	let _ = GoAbs(-3.5)
	let _ = GoRuneCount("hi")
	let _ = RuntimeJoin([]string{"a", "b"})
	let _ = SeedAppend(3)
	let _ = GlobalSum()
	let _ = GlobalPick(0)
	let _ = NegFold(-2)
	GenericUnit([]int{4})
	let _ = BadBagGlobalStop()
	let _ = BadBagSeedStop([]int{1})
}

func KeepSealed(r SealedRes) SealedRes { r }

func UseSealed(r SealedRes) SealedRes { KeepSealed(r) }

func MatchSealed(r SealedRes) int {
	match r {
	case SealedOk as v:
		v.Val
	case SealedZero:
		0
	}
}

func GoAbs(x float64) float64 { math.Abs(x) }

func GoRuneCount(s string) int { utf8.RuneCountInString(s) }

func RuntimeJoin(xs []string) string { strings.Join(xs, ",") }

func SeedAppend(n int) []int {
	let mut xs = []int{n}
	for i in 0..n {
		xs = xs.Append(i)
	}
	xs
}

func GlobalSum() int {
	let mut acc = s4r2Items.Len()
	for x in s4r2Items {
		acc = acc + x.V
	}
	acc
}

func GlobalPick(idx int) S4r2Item { s4r2Items[idx] }

func NegFold(x int) int {
	if x < 0 { -1 } else { x }
}

func GenericUnit[T](xs []T) {
	if xs.Len() > 0 {
		return
	}
}

func BadBagGlobalStop() int {
	s4r2Bags.Len()
}

func BadBagSeedStop(xs []int) []BadBag {
	let mut ys = []BadBag{BadBag{Xs: xs}}
	ys = ys.Append(BadBag{Xs: xs})
	ys
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module s4r2test\ntrust go s4r2test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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

	bases := []string{
		stdDir(),
		filepath.Join(stdDir(), "option"),
		filepath.Join(stdDir(), "collections"),
		filepath.Join(stdDir(), "core"),
		filepath.Join(stdDir(), "bits"),
		filepath.Join(stdDir(), "math"),
		filepath.Join(stdDir(), "strconv"),
		filepath.Join(stdDir(), "strings"),
		filepath.Join(stdDir(), "utf8"),
	}
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
	var stdReach []*check.Func
	for _, spec := range []struct{ pkg, fn string }{
		{"math", "Abs"},
		{"math", "MinInt64"},
		{"strings", "Repeat"},
		{"utf8", "RuneText"},
		{"bits", "Len"},
	} {
		p, err := check.Std.Import(spec.pkg)
		if err != nil {
			t.Fatal(err)
		}
		var found *check.Func
		for _, fn := range p.Funcs {
			if fn.Name == spec.fn {
				found = fn
				break
			}
		}
		if found == nil {
			t.Fatalf("std %s.%s not found", spec.pkg, spec.fn)
		}
		stdReach = append(stdReach, found)
	}
	const stdProgramDir = "/ergo-std-program"
	a := sketchfront.Check("func main() {}\n")
	su, err := sketchfront.IRSpikeUnit(a, filepath.Join(stdProgramDir, "std.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	su.Root = stdProgramDir
	if err := indexGoIR(t, paths, goIdx, irGoProgram{u: su, module: stdProgramDir, reach: stdReach}); err != nil {
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

	for _, want := range []string{
		"KeepSealed",
		"UseSealed",
		"MatchSealed",
		"GoAbs",
		"GoRuneCount",
		"RuntimeJoin",
		"SeedAppend",
		"GlobalSum",
		"GlobalPick",
		"NegFold",
		"GenericUnit",
		"math__Abs",
		"math__MinInt64",
		"strings__Repeat_1",
		"utf8__RuneText_1",
		"bits__Len_1",
	} {
		got, ok := emitted[want]
		if !ok {
			t.Fatalf("s4r2 build did not emit %s (stops: %v)", want, stops)
		}
		res := goIdx.compare(got.key, irWitnessSite{file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("s4r2 %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}
	for _, forbidden := range []string{"BadBagGlobalStop", "BadBagSeedStop"} {
		if _, ok := emitted[forbidden]; ok {
			t.Fatalf("unadmitted %s must stop honestly and not emit", forbidden)
		}
	}
	stopText := strings.Join(stops, "\n")
	for _, wantStop := range []string{"let of slice"} {
		if !strings.Contains(stopText, wantStop) {
			t.Fatalf("expected stop %q in %v", wantStop, stops)
		}
	}
}
