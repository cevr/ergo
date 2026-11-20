package lower

import (
	"go/constant"
	"math/rand"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// postLawSrc is an operation type with a post law (types.md Laws, Post
// laws: `{ Op(params); R() == X }`) and a provider built by a
// constructor, so a check build runs it from reached states (postRun).
const postLawSrc = `type PCell {
	Put(n int),
	Get() int,

	law PutGets(n int) {
		Put(n)
		Get() == n
	},
}

type PBox { s sync.Ref[int] } implements PCell

func NewPBox() PBox { PBox{s: sync.NewRef[int](0)} }

func Put(p PBox, n int) { p.s.Set(n) }

func Get(p PBox) int { p.s.Get() }
`

// TestLawRunsAgreeOnEveryPath (LAW-RUNS-PARITY): a rewrite check's build
// (checkUnit: site instances, post laws, imported runs) runs the
// same law runs on every test entry point, the text path and each IR one,
// over generated programs of rewrite laws (rwProgram), so moving the
// checks onto the IR build runs no fewer. Imported runs are checked over
// the examples (sketchfront TestCheckBuildsAgreeOnEveryPath).
//
// Mutants (lower mutants.json): lrp-instances-dropped, lrp-posts-dropped,
// lrp-imports-dropped.
func TestLawRunsAgreeOnEveryPath(t *testing.T) {
	kinds := map[string]bool{}
	for seed := int64(1); seed <= 6; seed++ {
		types := genRwTypes(rand.New(rand.NewSource(seed)))
		src := rwProgram(types) + postLawSrc
		file, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		pkg, diags := check.Check("main", []*syntax.File{file}, nil)
		if len(diags) > 0 {
			t.Fatalf("seed %d: %v\n%s", seed, diags, src)
		}
		// The check build of every law of the program, as RewriteChecksCached
		// makes it when the probe asks for all of them (checkUnit): its law
		// without a rewrite is a post law, and the instance laws are checked
		// at the spot (TestRewriteLawVerdicts' instances).
		var insts []RewriteInstance
		for _, law := range pkg.Laws {
			for _, ty := range types {
				for _, l := range ty.laws {
					if l.instance && law.Owner != nil && law.Owner.Name == ty.name && law.Name == l.name {
						insts = append(insts, RewriteInstance{Law: law, Args: []constant.Value{constant.MakeInt64(rwSpot)}})
					}
				}
			}
		}
		u := checkUnit(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}, pkg.Laws, insts)
		files, err := LowerTests(u)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		for k := range LawRunSet(files) {
			kind, _, _ := strings.Cut(k, " ")
			kinds[kind] = true
		}
		if err := CheckLawRunParity(u); err != nil {
			t.Fatalf("seed %d: %v\nreplay: seed %d of TestLawRunsAgreeOnEveryPath\n%s", seed, err, seed, src)
		}
	}
	if !kinds["run"] || !kinds["instance"] || !kinds["post"] {
		t.Fatalf("the seeds' check builds run %v; the property needs law runs, site instances and post laws", kinds)
	}
}
