package mutantderive_test

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/sketch/mutantderive"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

func checkSourceValid(t *testing.T, baseCodes map[string]int, m mutantderive.Mutant) {
	t.Helper()
	res, err := sketchfront.Format(m.Source, 0)
	if err != nil {
		t.Fatalf("mutant %s (%s line %d): Format failed: %v\nsource:\n%s", m.ID, m.Operator, m.Line, err, m.Source)
	}
	if res.Changed {
		t.Fatalf("mutant %s (%s line %d): not in canonical format (FK7a)\nsource:\n%s", m.ID, m.Operator, m.Line, m.Source)
	}
	a := sketchfront.Check(m.Source)
	afterCodes := map[string]int{}
	for _, d := range a.Diagnostics {
		afterCodes[d.Code]++
	}
	for code, count := range afterCodes {
		if count > baseCodes[code] {
			t.Fatalf("mutant %s (%s line %d): introduced diagnostic %s: %v\nsource:\n%s", m.ID, m.Operator, m.Line, code, a.Diagnostics, m.Source)
		}
	}
}

func diagCounts(a sketchfront.Analysis) map[string]int {
	m := map[string]int{}
	for _, d := range a.Diagnostics {
		m[d.Code]++
	}
	return m
}

func TestOperatorsAllFireAndTypeCheck(t *testing.T) {
	src := `type BadInput {}

type UnusedErr {}

type Color = Red{} | Green{} | Blue{}

func Compare(a int, b int) bool {
	a < b && a != 0
}

func SumRange(xs []int, n int) int {
	let mut total = 0
	total = total + 1
	for i in 0..n {
		total = total + i
	}
	let sub = xs[0:n]
	total + len(sub)
}

func ColorScore(c Color) int {
	match c {
	case Red:
		1
	case Green:
		2
	case Blue:
		3
	}
}

func MaybeDouble(x int) Option[int] {
	if x > 0 {
		return Some[int]{Value: x * 2}
	}
	None
}

func CheckPositive(x int) int fails BadInput | UnusedErr {
	if x < 0 {
		return BadInput
	}
	x
}

law PositiveCompare(a int where a > 0 && a < 100) {
	Compare(a, a+1)
}
`
	res, err := sketchfront.Format(src, 0)
	if err != nil {
		t.Fatalf("format fixture: %v", err)
	}
	src = res.Text
	base := sketchfront.Check(src)
	if len(base.Diagnostics) > 0 {
		t.Fatalf("baseline fixture has diagnostics: %v", base.Diagnostics)
	}
	baseCodes := diagCounts(base)

	mutants := mutantderive.GenerateSource("fixture.ergo", src)
	if len(mutants) == 0 {
		t.Fatalf("expected derived mutants, got 0")
	}

	seenOps := map[mutantderive.Operator]int{}
	seenIDs := map[string]bool{}
	baseNorm := syntax.CheckText(src)
	for _, m := range mutants {
		if m.ID == "" || m.Decl == "" || m.Line <= 0 || m.Operator == "" {
			t.Fatalf("incomplete mutant metadata: %+v", m)
		}
		if seenIDs[m.ID] {
			t.Fatalf("duplicate mutant ID: %s", m.ID)
		}
		seenIDs[m.ID] = true
		if syntax.CheckText(m.Source) == baseNorm {
			t.Fatalf("mutant %s is a token no-op", m.ID)
		}
		checkSourceValid(t, baseCodes, m)
		seenOps[m.Operator]++
	}

	wantOps := []mutantderive.Operator{
		mutantderive.OpCmpSwap,
		mutantderive.OpBoundOffByOne,
		mutantderive.OpDropStmt,
		mutantderive.OpMatchArmDefault,
		mutantderive.OpReturnZero,
		mutantderive.OpDropFails,
		mutantderive.OpWeakenLawPremise,
		mutantderive.OpFlipSomeNone,
	}
	for _, op := range wantOps {
		if seenOps[op] == 0 {
			t.Errorf("operator %q produced 0 mutants (got counts: %v)", op, seenOps)
		}
	}

	// Determinism check: generating twice produces identical IDs and sources.
	again := mutantderive.GenerateSource("fixture.ergo", src)
	if len(again) != len(mutants) {
		t.Fatalf("non-deterministic count: %d vs %d", len(mutants), len(again))
	}
	for i := range mutants {
		if mutants[i].ID != again[i].ID || mutants[i].Source != again[i].Source {
			t.Fatalf("non-deterministic mutant %d: %+v vs %+v", i, mutants[i], again[i])
		}
	}
}

func TestDropFailsNeverProducesUnviableMutant(t *testing.T) {
	// BadA IS returned in body, so dropping BadA must NOT be produced.
	// BadB is NOT returned in body, so dropping BadB SHOULD be produced.
	src := `type BadA {}

type BadB {}

func Check(x int) int fails BadA | BadB {
	if x < 0 {
		return BadA
	}
	x
}
`
	res, err := sketchfront.Format(src, 0)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	src = res.Text
	base := sketchfront.Check(src)
	if len(base.Diagnostics) > 0 {
		t.Fatalf("baseline diagnostics: %v", base.Diagnostics)
	}
	baseCodes := diagCounts(base)

	mutants := mutantderive.GenerateSource("fails.ergo", src)
	dropCount := 0
	for _, m := range mutants {
		checkSourceValid(t, baseCodes, m)
		if m.Operator == mutantderive.OpDropFails {
			dropCount++
			if !strings.Contains(m.Source, "fails BadA") || strings.Contains(m.Source, "BadB {") == false {
				// BadA must still be in the fails row; BadB must have been dropped from fails.
				if strings.Contains(m.Source, "fails BadB") {
					t.Fatalf("unexpected drop of required BadA: %s", m.Source)
				}
			}
		}
	}
	if dropCount != 1 {
		t.Fatalf("expected exactly 1 viable drop-fails mutant (dropping unused BadB), got %d", dropCount)
	}
}

func TestSeededPropertyAllDerivedMutantsTypeCheck(t *testing.T) {
	rng := rand.New(rand.NewSource(20261125))
	cmpOps := []string{"<", "<=", ">", ">=", "==", "!="}
	retTypes := []string{"int", "bool", "string", "Option[int]"}

	totalGenerated := 0
	for trial := 0; trial < 25; trial++ {
		var b strings.Builder
		b.WriteString("type ErrA {}\n\ntype ErrB {}\n\ntype Mode = Fast{} | Slow{} | Idle{}\n\n")
		nFuncs := 2 + rng.Intn(3)
		for f := 0; f < nFuncs; f++ {
			cmp1 := cmpOps[rng.Intn(len(cmpOps))]
			cmp2 := cmpOps[rng.Intn(len(cmpOps))]
			rt := retTypes[rng.Intn(len(retTypes))]
			fnName := fmt.Sprintf("Fn%d_%d", trial, f)
			switch rt {
			case "int":
				fmt.Fprintf(&b, `func %s(a int, b int, xs []int, m Mode) int {
	let mut acc = 0
	if a %s b {
		acc = acc + 1
	}
	for i in 0..3 {
		acc = acc + i
	}
	let sub = xs[0:len(xs)]
	let bonus = match m {
	case Fast:
		10
	case Slow:
		5
	case Idle:
		1
	}
	if a %s 0 {
		return acc + bonus + len(sub)
	}
	acc + bonus
}

`, fnName, cmp1, cmp2)
			case "bool":
				fmt.Fprintf(&b, `func %s(a int, b int) bool {
	let ok = a %s b
	ok && b %s 10
}

`, fnName, cmp1, cmp2)
			case "string":
				fmt.Fprintf(&b, `func %s(a int, b int) string {
	if a %s b {
		return "yes"
	}
	"no"
}

`, fnName, cmp1)
			case "Option[int]":
				fmt.Fprintf(&b, `func %s(a int, b int) Option[int] fails ErrA | ErrB {
	if a %s 0 {
		return ErrA
	}
	if a %s b {
		return Some[int]{Value: a}
	}
	None
}

`, fnName, cmp1, cmp2)
			}
		}
		fmt.Fprintf(&b, `law Prop%d(a int where a >= 0 && a <= 50, b int where b >= 0 && b <= 50) {
	a + b == b + a
}
`, trial)

		res, err := sketchfront.Format(b.String(), 0)
		if err != nil {
			t.Fatalf("trial %d: format failed: %v", trial, err)
		}
		src := res.Text
		base := sketchfront.Check(src)
		if len(base.Diagnostics) > 0 {
			t.Fatalf("trial %d: generated fixture has diagnostics: %v\n%s", trial, base.Diagnostics, src)
		}
		baseCodes := diagCounts(base)

		mutants := mutantderive.GenerateSource(fmt.Sprintf("trial_%d.ergo", trial), src)
		if len(mutants) == 0 {
			t.Fatalf("trial %d: expected >0 derived mutants", trial)
		}
		totalGenerated += len(mutants)
		for _, m := range mutants {
			checkSourceValid(t, baseCodes, m)
		}
	}
	if totalGenerated < 200 {
		t.Fatalf("expected >=200 derived mutants across 25 seeded trials, got %d", totalGenerated)
	}
}

func TestRealRepoPackageBoardAllDerivedMutantsValid(t *testing.T) {
	repoRoot := repoRootFromTest(t)
	pkgDir := filepath.Join(repoRoot, "modules", "board", "board")
	platform := gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	base, err := sketchfront.CheckDirForPlatform(pkgDir, platform)
	if err != nil || len(base.Diagnostics) > 0 {
		t.Fatalf("CheckDirForPlatform(%s): err=%v diags=%v", pkgDir, err, base.Diagnostics)
	}
	baseCodes := diagCounts(base)

	// Test on mentions.ergo (real repo file with comparisons, loops, indexing, options, laws).
	absFile := filepath.Join(pkgDir, "mentions.ergo")
	mutants := mutantderive.GeneratePackage(pkgDir, platform, base, mutantderive.Options{
		BaseRoot:   repoRoot,
		FilterFile: absFile,
	})
	if len(mutants) == 0 {
		t.Fatalf("expected derived mutants for mentions.ergo, got 0")
	}
	t.Logf("mentions.ergo: generated %d viable derived mutants", len(mutants))
	for i, m := range mutants {
		res, err := sketchfront.Format(m.Source, 0)
		if err != nil || res.Changed {
			t.Fatalf("mutant %s failed format check: err=%v changed=%v", m.ID, err, res.Changed)
		}
		diags, ok := base.CheckRootOverlay(m.AbsFile, m.Source)
		if !ok {
			t.Fatalf("CheckRootOverlay returned ok=false for %s", m.AbsFile)
		}
		afterCodes := map[string]int{}
		for _, d := range diags {
			afterCodes[d.Code]++
		}
		for code, count := range afterCodes {
			if count > baseCodes[code] {
				t.Fatalf("mutant %s (%s line %d) introduced diagnostic %s: %v", m.ID, m.Operator, m.Line, code, diags)
			}
		}
		// Cross-check the first mutant with full CheckDirFS too.
		if i == 0 {
			overlay := sketchfront.OverlayFS(map[string]string{filepath.Clean(m.AbsFile): m.Source})
			after, err := sketchfront.CheckDirFS(overlay, pkgDir, platform)
			if err != nil {
				t.Fatalf("mutant %s failed CheckDirFS: %v", m.ID, err)
			}
			for code, count := range diagCounts(after) {
				if count > baseCodes[code] {
					t.Fatalf("mutant %s introduced diagnostic under CheckDirFS %s: %v", m.ID, code, after.Diagnostics)
				}
			}
		}
	}
}

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}
