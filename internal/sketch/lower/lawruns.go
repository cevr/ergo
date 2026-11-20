package lower

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// LAW-RUNS-PARITY: a check build's law runs are the same on every test
// entry point (testLoweringOf). LawRunSet and CheckLawRunParity are the
// property's reading, shared by lower's and sketchfront's tests.

// The law runs a test build's Go text holds: each run's label (CachedRun),
// each rewrite site instance it checks, and each post law it runs from
// reached states (its ok line names the prefixes).
var (
	runLabel     = regexp.MustCompile(`CachedRun\("((?:[^"\\]|\\.)*)"\)`)
	runInstance  = regexp.MustCompile(`\.X?instances = append\([^,]+, "((?:[^"\\]|\\.)*)"\)`) // X: a split package's exported field
	runPostLabel = regexp.MustCompile(`"ok ((?:[^"\\]|\\.)*) \(\d+ cases, prefixes up to "`)
)

// LawRunSet is the law runs of a test build's files, each with its count:
// "run <label>", "instance <args>" and "post <label>".
func LawRunSet(files []File) map[string]int {
	out := map[string]int{}
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		text := string(f.Data)
		for kind, re := range map[string]*regexp.Regexp{"run": runLabel, "instance": runInstance, "post": runPostLabel} {
			for _, m := range re.FindAllStringSubmatch(text, -1) {
				out[kind+" "+m[1]]++
			}
		}
	}
	return out
}

// lawRunPaths are the test entry points a check build may be handed: the
// text path and each IR one.
var lawRunPaths = []struct {
	name  string
	lower func(Unit) ([]File, error)
}{
	{"LowerTests", LowerTests},
	{"LowerIRTests", func(u Unit) ([]File, error) { f, _, err := LowerIRTests(u); return f, err }},
	{"LowerIRSplit", func(u Unit) ([]File, error) { f, _, _, err := LowerIRSplit(u, true); return f, err }},
	{"LowerIRShards", func(u Unit) ([]File, error) { f, _, _, err := LowerIRShards(u, true); return f, err }},
	// The lowering a flat union's bail starts again (flatunion.go
	// lowerAgain), on the text path: with no bail it lowers the program
	// once more from its record.
	{"lowerAgain", func(u Unit) ([]File, error) {
		code, err := newTestLowerer(u).lowerAgain()
		return []File{{Path: "main.go", Data: code}}, err
	}},
	{"lowerIRTestParts", func(u Unit) ([]File, error) {
		code, _, _, err := lowerIRTestParts(u, "m")
		return []File{{Path: "main.go", Data: code}}, err
	}},
}

// CheckLawRunParity lowers the check build u on every test entry point
// and reports the first whose law runs differ from the text path's.
func CheckLawRunParity(u Unit) error {
	var want map[string]int
	for i, p := range lawRunPaths {
		files, err := p.lower(u)
		if err != nil {
			return fmt.Errorf("%s: %v", p.name, err)
		}
		got := LawRunSet(files)
		if i == 0 {
			want = got
			continue
		}
		if !maps.Equal(got, want) {
			var missing, extra []string
			for k, n := range want {
				if got[k] != n {
					missing = append(missing, fmt.Sprintf("%s ×%d (has %d)", k, n, got[k]))
				}
			}
			for k := range got {
				if _, ok := want[k]; !ok {
					extra = append(extra, k)
				}
			}
			slices.Sort(missing)
			slices.Sort(extra)
			return fmt.Errorf("%s runs other laws than LowerTests: differs at %s; not on the text path: %s", p.name, strings.Join(missing, "; "), strings.Join(extra, "; "))
		}
	}
	return nil
}
