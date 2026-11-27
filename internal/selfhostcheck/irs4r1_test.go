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

// TestIRS4R1Cutover kills lower-s4r1-* mutants: flat shared union slot
// accounting across repeated field types, payload holds checks, null JSON
// case lists on empty type/val slices, local variant record receivers,
// loop parameter field hoist guards, and std match defect sites. It
// builds Go because compiler/lower runs only via irdump.
func TestIRS4R1Cutover(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	const src = `import option

type Pair32 = PairZero{} | PairTwo{A int32, B int32}

type ErrUnion = ErrOne{Code int} | ErrTwo{Msg string}

type ScalarBox = ScalarEmpty{} | ScalarNum{V int}

type SliceBox = SliceEmpty{} | SliceVals{Xs []int}

type S4r1Shape = S4r1Circle{R int} | S4r1Square{S int}

type Policy { Limit int, Window int }

func main() {
	let _ = MatchPair32(PairTwo{A: 1, B: 2})
	let _ = MatchErr(ErrOne{Code: 3})
	let _ = CheckErr(true) catch { case _: 0 }
	let _ = UseScalarTwice(ScalarNum{V: 4})
	let _ = UseSliceTwiceStop(SliceVals{Xs: []int{1}})
	let _ = S4r1Circle{R: 5}.Area()
	let _ = LoopFieldReadStop(Policy{Limit: 1, Window: 2}, []int{3})
	let _ = OptionMatch(option.Some[S4r1Circle]{Value: S4r1Circle{R: 6}})
}

func MatchPair32(p Pair32) bool {
	match p {
	case PairTwo as t:
		t.A == t.B
	case PairZero:
		false
	}
}

func CheckErr(c bool) int fails ErrOne | ErrTwo {
	if c {
		return ErrOne{Code: 1}
	}
	1
}

func MatchErr(e ErrUnion) int {
	match e {
	case ErrOne as a:
		a.Code
	case ErrTwo as b:
		b.Msg.Len()
	}
}

func UseScalarTwice(b ScalarBox) int {
	match b {
	case ScalarNum as x:
		x.V + x.V
	case ScalarEmpty:
		0
	}
}

func UseSliceTwiceStop(b SliceBox) int {
	match b {
	case SliceVals as x:
		x.Xs.Len() + x.Xs.Len()
	case SliceEmpty:
		0
	}
}

func Area(c S4r1Circle) int {
	c.R * c.R
}

func LoopFieldReadStop(p Policy, xs []int) bool {
	for x in xs {
		if x > p.Limit {
			return false
		}
	}
	true
}

func OptionMatch(o option.Option[S4r1Circle]) int {
	match o {
	case option.Some[S4r1Circle] as c:
		c.Value.R
	case option.None:
		0
	}
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

	bases := []string{
		stdDir(),
		filepath.Join(stdDir(), "option"),
		filepath.Join(stdDir(), "collections"),
		filepath.Join(stdDir(), "core"),
		filepath.Join(stdDir(), "term"),
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
	termPkg, err := check.Std.Import("term")
	if err != nil {
		t.Fatal(err)
	}
	var termText *check.Func
	for _, fn := range termPkg.Funcs {
		if fn.Name == "text" {
			termText = fn
			break
		}
	}
	if termText == nil {
		t.Fatal("std term.text not found")
	}
	const stdProgramDir = "/ergo-std-program"
	a := sketchfront.Check("func main() {}\n")
	su, err := sketchfront.IRSpikeUnit(a, filepath.Join(stdProgramDir, "std.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	su.Root = stdProgramDir
	if err := indexGoIR(t, paths, goIdx, irGoProgram{u: su, module: stdProgramDir, reach: []*check.Func{termText}}); err != nil {
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

	for _, want := range []string{"MatchPair32", "MatchErr", "UseScalarTwice", "S4r1Circle.Area", "OptionMatch", "term__text"} {
		got, ok := emitted[want]
		if !ok {
			t.Fatalf("s4r1 build did not emit %s (stops: %v)", want, stops)
		}
		res := goIdx.compare(got.key, irWitnessSite{file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("s4r1 %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}
	for _, forbidden := range []string{"UseSliceTwiceStop", "LoopFieldReadStop"} {
		if _, ok := emitted[forbidden]; ok {
			t.Fatalf("unadmitted %s must stop honestly and not emit", forbidden)
		}
	}
	stopText := strings.Join(stops, "\n")
	for _, wantStop := range []string{"match: repeated read of a payload binding", "control: loop hoisted parameter field read"} {
		if !strings.Contains(stopText, wantStop) {
			t.Fatalf("expected stop %q in %v", wantStop, stops)
		}
	}
}
