package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// classHSources are the fixtures, the witnesses, the translated prototype
// fixtures and corpus (#196; the frozen old .sketch copies no longer
// parse) and sources of the generators: the domain of the class H
// properties.
func classHSources(t *testing.T) map[string]string {
	out := map[string]string{}
	for _, pattern := range []string{
		"testdata/fixtures/*/*.ergo",
		"testdata/classf/*.ergo",
		"../testdata/*.ergo",
		"../testdata/*/*.ergo",
		"../syntax/testdata/corpus/*/*.ergo",
		"../syntax/testdata/corpus/*/*/*.ergo",
	} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			out[f] = string(data)
		}
	}
	for seed := range uint64(40) {
		out[fmt.Sprintf("admission/errors/%d", seed)] = recursiveAdmission(seed, true).src
		out[fmt.Sprintf("admission/capabilities/%d", seed)] = recursiveAdmission(seed, false).src
		out[fmt.Sprintf("generic/%d", seed)] = genericInferred(seed)
		out[fmt.Sprintf("keyed/%d", seed)] = keyedLiterals(seed)
	}
	return out
}

func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestRefWalkComplete: the class H reference walk visits every node of
// every body and resolves every identifier as typing does (round 16).
func TestRefWalkComplete(t *testing.T) {
	sources := classHSources(t)
	for _, name := range sortedNames(sources) {
		if p := RefWalkProblems(sources[name]); len(p) > 0 {
			t.Errorf("%s:\n\t%s", name, strings.Join(p, "\n\t"))
		}
	}
	t.Logf("%d sources", len(sources))
}
