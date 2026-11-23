package lower

import (
	"path"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// TestStdPackagesLowerAsTestRoots: every std package, as the root of a test
// build (ergo test std:PKG, NATIVE-LAWS slice 2), lowers and its Go
// type-checks, so its own laws can run natively on its source. A law of an
// extern Go type (rand.Stream's IntNInRange, time.Timeline's) calls the
// bound value's runtime method, with no task: before, it was lowered as a
// capability call that takes the task first, and std rand and time did not
// type-check. Mutant: lower-extern-law-op-task.
func TestStdPackagesLowerAsTestRoots(t *testing.T) {
	var programs []*lowered
	for _, p := range stdlib.Packages() {
		pkg, err := check.Std.Import(p)
		if err != nil {
			t.Errorf("std %s: %v", p, err)
			continue
		}
		srcs, _ := stdlib.Package(p)
		u := Unit{Package: pkg, Files: pkg.Files()}
		for _, f := range u.Files {
			u.Sources = append(u.Sources, Source{Path: path.Join("/std", p, f.Name), Text: srcs[f.Name]})
		}
		files, err := LowerTests(u)
		if err != nil {
			t.Errorf("std %s as a test root: %v", p, err)
			continue
		}
		programs = append(programs, &lowered{name: "std_" + strings.ReplaceAll(p, "/", "_"), files: files})
	}
	typecheckGo(t, programs)
}
