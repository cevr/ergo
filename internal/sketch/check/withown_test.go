package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The context ownership fact (runtime-199 stage 3b; resources.md, Context
// ownership): per with site, the entries it acquires in acquisition order,
// each with its resolved Close, the release needs charged at the with
// site, and the located refusals.

const ownPrelude = `type A { Use() }
type B { Use() }
type Log { Say(s string) }
type First {}
type Second {}
type Pair {}
type Other {}
type Quiet {}
func Use(_ First)  {}
func Use(_ Second) {}
func Use(_ Pair)   {}
func Use(_ Other)  {}
func Say(_ Quiet, s string) {}
func Close(_ First)  {}
func Close(_ Pair)   {}
func Close(_ Other)  {}
`

// ownFiles is the parsed file of each package checkOwn checked.
var ownFiles = map[*check.Package]*syntax.File{}

func checkOwn(t *testing.T, src string) (*check.Package, []check.Diagnostic) {
	t.Helper()
	var imports, rest []string
	for _, line := range strings.SplitAfter(src, "\n") {
		if strings.HasPrefix(line, "import ") {
			imports = append(imports, line)
		} else {
			rest = append(rest, line)
		}
	}
	f, err := syntax.Parse(strings.Join(imports, "") + ownPrelude + strings.Join(rest, ""))
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	ownFiles[pkg] = f
	return pkg, ds
}

type ownImporter struct{ pkg *check.Package }

func (i ownImporter) Import(path string) (*check.Package, error) {
	if path == "makers" {
		return i.pkg, nil
	}
	return check.Std.Import(path)
}
