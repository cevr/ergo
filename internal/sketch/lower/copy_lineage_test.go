package lower

import (
	"regexp"
	"strings"
	"testing"
)

// TestChainSkipsLineage: an Append on a one-extender site's result
// (handover.go, own-lineage) is not a chain link written in place (V8,
// inPlaceChain): the site may extend that array in place later, past the
// result's length, where the chain would have written. The promised fact
// is the outer Append's clipped source. Mutant: inPlaceChain takes a
// lineage receiver (mapcopy-chain-lineage). The parameter is read whole
// (size), so it is not owned by parts (own-parts, parts.go).
func TestChainSkipsLineage(t *testing.T) {
	src := "type State { Log []int }\n\nfunc size(s State) int { len(s.Log) }\n\nfunc f(s State, a int) State {\n\tState{Log: s.Log.Append(a + size(s)).Append(a + 1)}\n}\n\n" +
		"func main() needs Console {\n\tConsole.Println(\"\\(len(f(State{Log: []int{}}, 1).Log))\")\n}\n"
	body := goFuncBody(t, mustLower(t, "chainlineage", src).mainGo(), "f")
	if !strings.Contains(body, "LineageAppend[") {
		t.Fatalf("the inner Append is not a one-extender site:\n%s", body)
	}
	if !strings.Contains(body, "slices.Clip(") {
		t.Errorf("the outer Append writes the lineage result in place:\n%s", body)
	}
}

// TestNamedTailsSettle: a function passed by name (here to maps.Update)
// whose tail is a one-extender Append takes the plain clipped Append
// (settleNamedTails), so its result is fresh; a function only called
// keeps the one-extender site (own-lineage), its promised form. Mutants:
// mapcopy-named-lineage-off (TestCopyCountsMatchModel also kills it),
// mapcopy-named-lineage-callee. keep reads s whole (size), so s is
// not owned by parts (own-parts, parts.go).
func TestNamedTailsSettle(t *testing.T) {
	src := "import maps\n\ntype State { Log []int }\n\nfunc size(s State) int { len(s.Log) }\n\nfunc keep(s State) []int { s.Log.Append(size(s)) }\n\n" +
		"func cb(o Option[[]int]) []int {\n\tmatch o {\n\tcase Some[[]int] as v:\n\t\tv.Value.Append(9)\n\tcase None:\n\t\t[]int{}\n\t}\n}\n\n" +
		"func main() needs Console {\n\tlet mut m = Map[string, []int]{\"a\": []int{1}}\n\tm = maps.Update(m, \"a\", cb)\n\tConsole.Println(\"\\(len(m)) \\(len(keep(State{Log: []int{}})))\")\n}\n"
	mainGo := mustLower(t, "namedtails", src).mainGo()
	if body := goFuncBody(t, mainGo, "keep"); !strings.Contains(body, "LineageAppend[") {
		t.Errorf("a function only called lost its one-extender site:\n%s", body)
	}
	if body := goFuncBody(t, mainGo, "cb"); strings.Contains(body, "LineageAppend[") {
		t.Errorf("a callback by name keeps its one-extender site, so its result is not fresh:\n%s", body)
	}
}

// TestNamedTailsSettleAcrossPackages: a library function passed by name
// from the root package (maps.Update(m, k, cbs.Start)) settles its
// one-extender tail as one passed in its own package does; a library
// function only called keeps it. Mutant: passedByName reads only the
// function's own package (mapcopy-named-lineage-own-package).
func TestNamedTailsSettleAcrossPackages(t *testing.T) {
	lib := checkPackage(t, "cbs", []pkgSource{
		{"/ergo-test/cbs/a.ergo", "type State { Log []int }\n\nfunc size(s State) int { len(s.Log) }\n\nfunc Keep(s State) []int { s.Log.Append(size(s)) }\n\n" +
			"func Start(o Option[[]int]) []int {\n\tmatch o {\n\tcase Some[[]int] as v:\n\t\tv.Value.Append(9)\n\tcase None:\n\t\t[]int{}\n\t}\n}\n"},
	}, nil)
	root := checkPackage(t, "main", []pkgSource{
		{"/ergo-test/main.ergo", "import maps\nimport cbs\n\n" +
			"func main() needs Console {\n\tlet mut m = Map[string, []int]{\"a\": []int{1}}\n\tm = maps.Update(m, \"a\", cbs.Start)\n\tConsole.Println(\"\\(len(m)) \\(len(cbs.Keep(cbs.State{Log: []int{}})))\")\n}\n"},
	}, pkgImporter{"cbs": lib.Package})
	root.Imports = []Unit{lib}
	src := mustLowerUnit(t, "namedtailspkg", root, false).mainGo()
	keep, start := goFuncNamed(t, src, "Keep"), goFuncNamed(t, src, "Start")
	if !strings.Contains(keep, "LineageAppend[") {
		t.Errorf("a library function only called lost its one-extender site:\n%s", keep)
	}
	if strings.Contains(start, "LineageAppend[") {
		t.Errorf("a library callback passed by name from the root keeps its one-extender site:\n%s", start)
	}
	if body := goFuncNamed(t, src, "main_1"); strings.Contains(body, "maps.Clone(") {
		t.Errorf("m = maps.Update(m, k, cbs.Start) copies the Map:\n%s", body)
	}
}

// goFuncNamed is the Go text of the one function of src whose name ends
// in name (a library's functions carry its package's prefix).
func goFuncNamed(t *testing.T, src, name string) string {
	t.Helper()
	var found []string
	for _, part := range strings.Split(src, "\nfunc ")[1:] {
		// A function whose first parameter is a local type is a method.
		m := regexp.MustCompile(`^(\([^)]*\) )?(\w+)\(`).FindStringSubmatch(part)
		if m != nil && strings.HasSuffix(m[2], name) {
			found = append(found, part)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d Go functions named …%s in\n%s", len(found), name, src)
	}
	return found[0]
}
