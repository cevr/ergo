package sketchfront

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestCoreLaws verifies NO-PRELUDE CORE-LAWS:
//  1. std core's member laws run for every built-type function-form claim
//     ([]T, Map[K, V], string) with zero skips and pass in eval.
//  2. Broken function-form claims (Append dropping an element, Get always
//     missing, Len returning -1) are refuted and name the written claim.
//  3. User nominal types claiming core.Appender run core's inherited member
//     laws (passing when sound, refuted naming the claim when broken) and
//     lower cleanly on the IR path with zero fallbacks.
func TestCoreLaws(t *testing.T) {
	t.Run("std core", func(t *testing.T) {
		pkg, err := check.Std.Import("core")
		if err != nil {
			t.Fatalf("check.Std.Import(core): %v", err)
		}
		b := eval.DefaultBudget()
		b.Seed = 1
		rep, err := eval.Run(pkg, b)
		if err != nil {
			t.Fatal(err)
		}
		passed := map[string]bool{}
		for _, res := range rep.Results {
			if res.Status != eval.Passed {
				t.Fatalf("core law did not pass in eval: %+v", res)
			}
			passed[res.Name] = true
		}
		for _, want := range []string{
			"law Lengthed.LenIsNotNegative for []T at []int",
			"law Lengthed.LenIsNotNegative for Map[K, V] at Map[int, int]",
			"law Lengthed.LenIsNotNegative for string",
			"law Appender.AppendGrowsByOne for []T at []int",
			"law Appender.ReserveKeepsLength for []T at []int",
			"law Appender.ReserveIsUnobservable for []T at []int",
			"law View.SliceIsWindow for []T at []int",
			"law View.TakeAtIsIndexAndRest for []T at []int",
			"law SliceAppendIsLast",
			"law Joins.ConcatAssociates for []T at []int",
			"law Joins.ConcatAddsLengths for []T at []int",
			"law Joins.RepeatIsConcat for []T at []int",
			"law Lookup.GetIsConsistent for Map[K, V] at Map[int, int]",
			"law MapWriter.GetAfterPut for Map[K, V] at Map[int, int]",
			"law MapWriter.GetAfterDelete for Map[K, V] at Map[int, int]",
			"law MapWriter.PutKeepsOthers for Map[K, V] at Map[int, int]",
			"law MapWriter.TakeIsGetAndDelete for Map[K, V] at Map[int, int]",
			"law MapWriter.CloneIsEqual for Map[K, V] at Map[int, int]",
		} {
			if !passed[want] {
				t.Errorf("missing passed verdict %q; got %v", want, passed)
			}
		}
		dir, err := filepath.Abs(filepath.Join("..", "sketch", "std", "core"))
		if err != nil {
			t.Fatal(err)
		}
		a, err := CheckDir(dir)
		if err != nil || len(a.Diagnostics) != 0 {
			t.Fatalf("core dir must check cleanly: %v %v", err, a.Diagnostics)
		}
		u, err := analysisUnit(a, filepath.Join(dir, "core.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		u.NoFallback = true
		if _, report, err := lower.LowerIRTests(u); err != nil || len(report.Fallback) != 0 {
			t.Fatalf("core IR test lowering must have zero fallbacks: err=%v fallback=%v", err, report.Fallback)
		}
	})

	t.Run("broken function-form claims refuted naming claim", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			src       string
			wantLaw   string
			wantBlame string
		}{
			{
				name: "Append drops element",
				src: `type Sized {
	Len() int,
	law LenNonNeg(x Self) { x.Len() >= 0 },
}

extern go len func Len[T](xs []T) int implements Sized

type Appender[E] {
	..Sized,
	Append(e E) Self,
	law AppendGrowsByOne(x Self, e E) { x.Append(e).Len() == x.Len()+1 },
}

func Append[T](xs []T, e T) []T implements Appender[T] {
	let _ = e
	xs
}
`,
				wantLaw:   "law Appender.AppendGrowsByOne for []T at []int",
				wantBlame: "; []T claims Appender[T] at ",
			},
			{
				name: "Get always misses",
				src: `type Sized {
	Len() int,
	law LenNonNeg(x Self) { x.Len() >= 0 },
}

extern go len func Len[K comparable, V](m Map[K, V]) int implements Sized

type Lookup[K, V] {
	..Sized,
	Get(k K) Option[V],
	law GetConsistent(m Self, k K) { m.Get(k) == m.Get(k) },
}

type MapWriter[K, V] {
	..Lookup[K, V],
	Put(k K, v V) Self,
	law GetAfterPut(m Self, k K, v V) {
		match m.Put(k, v).Get(k) {
		case Some[V] as x:
			x.Value == v
		case None:
			false
		}
	},
}

func Get[K comparable, V](m Map[K, V], k K) Option[V] implements Lookup[K, V] {
	let _ = m
	let _ = k
	None
}

func Put[K comparable, V](m Map[K, V], k K, v V) Map[K, V] implements MapWriter[K, V] { m.Put(k, v) }
`,
				wantLaw:   "law MapWriter.GetAfterPut for Map[K, V] at Map[int, int]",
				wantBlame: "; Map[K, V] claims MapWriter[K, V] at ",
			},
			{
				name: "Len returns negative",
				src: `type Sized {
	Len() int,
	law LenIsNotNegative(x Self) { x.Len() >= 0 },
}

func Len(s string) int implements Sized {
	let _ = s
	-1
}
`,
				wantLaw:   "law Sized.LenIsNotNegative for string",
				wantBlame: "; string claims Sized at ",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module badclaim\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "bad.ergo"), []byte(tc.src), 0600); err != nil {
					t.Fatal(err)
				}
				a, err := CheckDir(dir)
				if err != nil || len(a.Diagnostics) != 0 {
					t.Fatalf("fixture must check cleanly: %v %v", err, a.Diagnostics)
				}
				b := eval.DefaultBudget()
				b.Seed = 1
				rep, err := a.Evaluate(b)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, res := range rep.Results {
					if res.Name != tc.wantLaw {
						continue
					}
					found = true
					if res.Status != eval.Refuted {
						t.Fatalf("expected %s to be Refuted, got %+v", tc.wantLaw, res)
					}
					if !strings.Contains(res.Message, tc.wantBlame) {
						t.Fatalf("refutation must name the written claim %q; got %q", tc.wantBlame, res.Message)
					}
				}
				if !found {
					t.Fatalf("did not find verdict for %q in %+v", tc.wantLaw, rep.Results)
				}
			})
		}
	})

	t.Run("user nominal claimant inherits core.Appender laws", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ergo.mod"), []byte("module userbag\n"), 0600); err != nil {
			t.Fatal(err)
		}
		const src = `import core

type Bag { items []int } implements core.Appender[int]

func Len(b Bag) int { b.items.Len() }

func Append(b Bag, e int) Bag { Bag{items: b.items.Append(e)} }

func Reserve(b Bag, extra int) Bag {
	let _ = extra
	b
}

type BadBag { items []int } implements core.Appender[int]

func Len(b BadBag) int { b.items.Len() }

func Append(b BadBag, e int) BadBag {
	let _ = e
	b
}

func Reserve(b BadBag, extra int) BadBag {
	let _ = extra
	b
}
`
		if err := os.WriteFile(filepath.Join(dir, "bag.ergo"), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
		a, err := CheckDir(dir)
		if err != nil || len(a.Diagnostics) != 0 {
			t.Fatalf("userbag must check cleanly: %v %v", err, a.Diagnostics)
		}
		b := eval.DefaultBudget()
		b.Seed = 1
		rep, err := a.Evaluate(b)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]eval.Result{}
		for _, res := range rep.Results {
			got[res.Name] = res
		}
		for _, wantPass := range []string{
			"law core.Lengthed.LenIsNotNegative for Bag",
			"law core.Appender.AppendGrowsByOne for Bag",
			"law core.Appender.ReserveKeepsLength for Bag",
			"law core.Appender.ReserveIsUnobservable for Bag",
		} {
			res, ok := got[wantPass]
			if !ok || res.Status != eval.Passed {
				t.Fatalf("expected %q to pass, got %+v (all: %+v)", wantPass, res, rep.Results)
			}
		}
		bad, ok := got["law core.Appender.AppendGrowsByOne for BadBag"]
		if !ok || bad.Status != eval.Refuted || !strings.Contains(bad.Message, "; BadBag claims core.Appender[int] at ") {
			t.Fatalf("expected BadBag to be refuted naming claim, got %+v", bad)
		}
		u, err := analysisUnit(a, filepath.Join(dir, "bag.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		u.NoFallback = true
		if _, report, err := lower.LowerIRTests(u); err != nil || len(report.Fallback) != 0 {
			t.Fatalf("userbag IR test lowering must have zero fallbacks: err=%v fallback=%v", err, report.Fallback)
		}
	})
}

// TestCoreLawsRunNatively compiles std core's member laws into a native test
// binary linked against runtime/v1 and executes it. This exercises runtime/v1's
// collection primitives (Reserve, SlicesTake, SlicesSlice, MapTake, MapsClone,
// ...) and lower's claim-law helper naming directly under core's Ergo laws.
func TestCoreLawsRunNatively(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("..", "sketch", "std", "core"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := CheckDir(dir)
	if err != nil || len(a.Diagnostics) != 0 {
		t.Fatalf("core dir must check cleanly: %v %v", err, a.Diagnostics)
	}
	tc := build.HostToolchain()
	tc.Profile = build.Dev
	var stderr bytes.Buffer
	output := filepath.Join(t.TempDir(), "core.test")
	cache := t.TempDir()
	_, err = Build(a, BuildRequest{
		Mode:      build.ModeTest,
		Source:    filepath.Join(dir, "core.ergo"),
		Output:    output,
		Cache:     build.Cache{Root: cache},
		Toolchain: tc,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("native build of std core failed: %v\n%s", err, stderr.String())
	}
	out, err := exec.Command(output, "--seed", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("native std core laws failed: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{
		"ok law Appender.ReserveIsUnobservable for []T at []int",
		"ok law View.TakeAtIsIndexAndRest for []T at []int",
		"ok law MapWriter.TakeIsGetAndDelete for Map[K, V] at Map[int, int]",
		"PASS (18 tests)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("native std core output missing %q:\n%s", want, text)
		}
	}
}
