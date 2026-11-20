package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

const lawGenDom = `import tasks

type Count {
	N() int,

	law NotNegative() { N() >= 0 },
}

type Lock {
	Hold() bool,

	law HoldIsStable() { Hold() == Hold() },
}

func Doubled(xs []int) []int { tasks.Transform(xs, 2, func(x int) int { 2 * x }) }
`

const lawGenUser = `import dom
import fs

type Reader { files fs.Files } implements dom.Count

func N(r Reader) int { 0 }

type FileLock { host fs.Host } implements dom.Lock

func Hold(l FileLock) bool { true }
`

// TestLawGenBuilderIsNamed is the smallest repro of LAW-GEN-NAMING (LD3's
// G12): the test build of a package whose law draws an fs.Files field
// calls its builder, std fstest.NewMemory, which the program reaches only
// through std imports (dom → tasks → testing → fstest). The naming pass
// named no such function, and LowerTests stopped with S109 ("NewMemory
// (func) has no name from the naming pass").
func TestLawGenBuilderIsNamed(t *testing.T) {
	dom := cxPackage(t, "dom", lawGenDom)
	imports := cxImporter{"dom": dom}
	f, err := syntax.Parse(lawGenUser)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("user", []*syntax.File{f}, imports)
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	if _, err := LowerTests(Unit{Package: pkg, Files: []*syntax.File{f}, Sources: []Source{{Path: testSourcePath, Text: lawGenUser}}, Imports: imports.units()}); err != nil {
		t.Fatal(err)
	}
}
