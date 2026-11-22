package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The param-free arm property (GEN9-GAP42 D; types.md Generics 9,
// resources.md R44). A marked generic body over an own generic union
// releases nothing of T, so it may drop the subject only on an arm whose
// variant holds no value of T: an empty variant, or one that holds only a
// concrete resource, which is released as any value's. An arm that drops a
// variant holding T, directly, through a record field or through an
// Option[T], is S108 Gap 42, as is a drop of the whole union.
//
// Each seed declares a union gU[T own] over a choice of the variants below,
// a generic pick[T own] and its concrete twin pickRes over gU[Res] with the
// same arms, and one test per variant and form that picks a value of that
// variant and finishes the result. Every Close writes to one journal.
//
// The model: the checker's verdict is S108 iff some arm drops a variant
// that holds T (pfVerdictModel); an admitted program's journal is the
// model's for each variant (pfJournal), the same for pick and pickRes.
const (
	pfPayload = iota // gP[T]{V T}: holds T
	pfEmpty          // gE: holds nothing
	pfNested         // gN[T]{O Option[T]}: holds T through an Option
	pfRecord         // gB[T]{V T, K int}: holds T through a field
	pfConcrete       // gC{R Res}: holds a concrete resource only
	pfKinds
)

var pfNames = [pfKinds]string{"gP", "gE", "gN", "gB", "gC"}

// pfHoldsT is whether the variant holds a value of T.
func pfHoldsT(k int) bool { return k == pfPayload || k == pfNested || k == pfRecord }

type pfCase struct {
	seed     int
	kinds    []int
	drop     map[int]bool // the arms that drop their variant (bind nothing)
	admitted bool
	src      string
	tests    []string
}

const pfPrelude = `import sync

type Res { id int } implements Closer

let journal = sync.NewRef[[]string]([]string{})

func say(s string) { journal.Update(func(xs []string) []string { xs.Append(s) }) }

func Close(r Res) { say("close \(r.id)") }

type gP[T own] { V T }

type gE {}

type gN[T own] { O Option[T] }

type gB[T own] { V T, K int }

type gC { R Res }

func finish(r own Option[Res]) {
	match r {
	case Some{Value: x}:
		Close(x)
	case None:
	}
}
`

// genParamFree is the program of one seed.
func genParamFree(seed int) *pfCase {
	r := rand.New(rand.NewPCG(uint64(seed), 0x9a42))
	c := &pfCase{seed: seed, drop: map[int]bool{}}
	c.kinds = []int{pfPayload}
	for k := pfEmpty; k < pfKinds; k++ {
		if r.IntN(2) == 0 {
			c.kinds = append(c.kinds, k)
		}
	}
	if len(c.kinds) == 1 {
		c.kinds = append(c.kinds, pfEmpty)
	}
	for _, k := range c.kinds {
		// An empty or concrete variant binds no T: its arm drops it. A
		// variant that holds T hands it on, or, one time in four, drops it.
		c.drop[k] = !pfHoldsT(k) || r.IntN(4) == 0
	}
	c.admitted = pfVerdictModel(c)
	var b strings.Builder
	b.WriteString(pfPrelude)
	var vs []string
	for _, k := range c.kinds {
		if k == pfEmpty || k == pfConcrete {
			vs = append(vs, pfNames[k])
		} else {
			vs = append(vs, pfNames[k]+"[T]")
		}
	}
	fmt.Fprintf(&b, "\ntype gU[T own] = %s\n", strings.Join(vs, " | "))
	b.WriteString("\nfunc pick[T own](o own gU[T]) Option[T] {\n" + pfArms(c) + "}\n")
	b.WriteString("\nfunc pickRes(o own gU[Res]) Option[Res] {\n" + pfArms(c) + "}\n")
	for i, k := range c.kinds {
		for _, fn := range []string{"pick", "pickRes"} {
			name := fmt.Sprintf("V%s_%s", pfNames[k], fn)
			c.tests = append(c.tests, name)
			id := 10 + i
			quoted := []string{}
			for _, l := range pfJournal(k, c.drop[k], id) {
				quoted = append(quoted, strconv.Quote(l))
			}
			fmt.Fprintf(&b, "\ntest %s {\n\tjournal.Set([]string{})\n\tlet o gU[Res] = %s\n\tlet r = %s(o)\n\tsay(\"picked\")\n\tfinish(r)\n\tassert journal.Get() == []string{%s}\n}\n",
				name, pfValue(k, id), fn, strings.Join(quoted, ", "))
		}
	}
	c.src = b.String()
	return c
}

// pfArms is the match of pick's body.
func pfArms(c *pfCase) string {
	var b strings.Builder
	b.WriteString("\tmatch o {\n")
	for _, k := range c.kinds {
		n := pfNames[k]
		switch {
		case c.drop[k]:
			fmt.Fprintf(&b, "\tcase %s:\n\t\tNone\n", n)
		case k == pfPayload, k == pfRecord:
			fmt.Fprintf(&b, "\tcase %s{V: v}:\n\t\tSome{Value: v}\n", n)
		case k == pfNested:
			fmt.Fprintf(&b, "\tcase %s{O: x}:\n\t\tx\n", n)
		}
	}
	b.WriteString("\t}\n")
	return b.String()
}

// pfValue is a value of variant k holding the resource id.
func pfValue(k, id int) string {
	switch k {
	case pfPayload:
		return fmt.Sprintf("gP[Res]{V: Res{id: %d}}", id)
	case pfEmpty:
		return "gE"
	case pfNested:
		return fmt.Sprintf("gN[Res]{O: Some[Res]{Value: Res{id: %d}}}", id)
	case pfRecord:
		return fmt.Sprintf("gB[Res]{V: Res{id: %d}, K: 1}", id)
	}
	return fmt.Sprintf("gC{R: Res{id: %d}}", id)
}

// pfVerdictModel: admitted iff no arm drops a variant that holds T.
func pfVerdictModel(c *pfCase) bool {
	for _, k := range c.kinds {
		if c.drop[k] && pfHoldsT(k) {
			return false
		}
	}
	return true
}

// pfJournal is the model's journal of a test of variant k (admitted): a
// dropped concrete variant is released in the pick, before "picked"; a
// handed-on T is closed by finish, after it; an empty variant closes
// nothing.
func pfJournal(k int, drop bool, id int) []string {
	closed := fmt.Sprintf("close %d", id)
	switch {
	case k == pfEmpty:
		return []string{"picked"}
	case k == pfConcrete:
		return []string{closed, "picked"}
	case drop:
		return nil // refused: not run
	}
	return []string{"picked", closed}
}

func pfSeeds(t *testing.T) []int {
	if s := os.Getenv("ERGO_PF_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("ERGO_PF_SEED %q: %v", s, err)
		}
		return []int{n}
	}
	var seeds []int
	for s := 1; s <= 60; s++ {
		seeds = append(seeds, s)
	}
	return seeds
}

// TestParamFreeArmsVerdict is the property's checker half: for each seed,
// the checker reports S108 Gap 42 iff the model refuses, and only on
// pick, never on its concrete twin. A failing seed prints the source and
// the replay command (ERGO_PF_SEED=n).
//
// Mutants only it kills (check mutants.json): gap42-tpart-variant-free (a
// variant holding T, through a field or an Option, counted T-free) and
// gap42-mixed-union-drop-admitted (a union that also holds a concrete
// resource dropped whole in a generic body, its T part leaked).
func TestParamFreeArmsVerdict(t *testing.T) {
	reached := map[bool]int{}
	for _, seed := range pfSeeds(t) {
		c := genParamFree(seed)
		f, err := syntax.Parse(c.src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, numbered(c.src))
		}
		_, diags := check.Check("main", []*syntax.File{f}, nil)
		var gap42, other []string
		for _, d := range diags {
			if d.Code == check.CodeType && strings.Contains(d.Message, "Gap 42") {
				gap42 = append(gap42, fmt.Sprintf("%d:%d %s", d.Line, d.Column, d.Message))
				continue
			}
			other = append(other, fmt.Sprintf("%d:%d %s %s", d.Line, d.Column, d.Code, d.Message))
		}
		reached[c.admitted]++
		replay := fmt.Sprintf("replay: ERGO_PF_SEED=%d go test -run '^TestParamFreeArms' ./internal/sketch/lower", seed)
		if len(other) > 0 {
			t.Fatalf("seed %d: diagnostics outside the property: %v\n%s\n%s", seed, other, numbered(c.src), replay)
		}
		if got := len(gap42) == 0; got != c.admitted {
			t.Errorf("seed %d: the checker admits=%v, the model admits=%v (drops %v): %v\n%s\n%s", seed, got, c.admitted, c.drop, gap42, numbered(c.src), replay)
		}
	}
	if len(pfSeeds(t)) > 1 && (reached[true] == 0 || reached[false] == 0) {
		t.Errorf("the seeds reach admitted=%d refused=%d: both verdicts must be reached", reached[true], reached[false])
	}
}

// TestParamFreeArmsRun is the property's run half: each admitted seed's
// tests, which assert the model's journal for pick and for pickRes, pass
// at check time (the evaluator), in the text lowering's test build and in
// the IR lowering's.
//
// It builds Go because its subject is the release each lowering emits on
// the dropped arm, generic and concrete: go/types says only that the
// lowered Go compiles, not which Close it runs.
//
// Mutant only it kills (check mutants.json): gap42-concrete-release-skipped
// (a T-free arm's concrete resource not released).
func TestParamFreeArmsRun(t *testing.T) {
	needsBuild(t)
	var cases []*pfCase
	var builds []*lowered
	for _, seed := range pfSeeds(t) {
		c := genParamFree(seed)
		if !c.admitted {
			continue
		}
		cases = append(cases, c)
		text := lowerTestBuild(t, fmt.Sprintf("pf_text_%d", seed), c.src)
		builds = append(builds, text)
		u := Unit{Package: text.pkg, Files: []*syntax.File{text.file}, Sources: []Source{{Path: testSourcePath, Text: c.src}}}
		files, _, err := LowerIRTests(u)
		if err != nil {
			t.Fatalf("seed %d: LowerIRTests: %v\n%s", seed, err, numbered(c.src))
		}
		builds = append(builds, &lowered{name: fmt.Sprintf("pf_ir_%d", seed), src: c.src, files: files})
	}
	if len(cases) == 0 {
		t.Fatal("no admitted seed")
	}
	for _, c := range cases {
		rep, err := evaluate(c.src)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", c.seed, err, numbered(c.src))
		}
		for _, res := range rep.Results {
			if string(res.Status) != "passed" {
				t.Errorf("seed %d: the evaluator: %s %s %s\n%s\nreplay: ERGO_PF_SEED=%d", c.seed, res.Name, res.Status, res.Reason, numbered(c.src), c.seed)
			}
		}
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, c := range cases {
		for _, form := range []string{"text", "ir"} {
			stdout, stderr, code := runExeArgs(t, exes[fmt.Sprintf("pf_%s_%d", form, c.seed)])
			for _, name := range c.tests {
				if !strings.Contains(stdout, "ok "+name) {
					t.Errorf("seed %d (%s lowering): test %s did not pass (exit %d)\n%s\n%s\n%s\nreplay: ERGO_PF_SEED=%d", c.seed, form, name, code, stdout, firstLines(stderr, 8), numbered(c.src), c.seed)
				}
			}
		}
	}
}
