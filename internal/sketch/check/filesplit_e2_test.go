package check_test

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The file-placement guard (stage E2 round 2). A diagnostic names the file
// it is reported in (Diagnostic.File), and errorf takes that file from the
// checker's current file. A pass that reports without first selecting the
// file it inspects reports against whichever file an earlier pass visited
// last, at an offset of the wrong source: the editor and the CLI then point
// at a construct that is not there (E1 round 2 M2, E2 round 1 M1).
//
// The invariant: adding a neutral file to a package moves no diagnostic.
// Every negative program the checker has (the corpus, which holds the
// acceptance rule mirror, the fixture mirror and the parser's corpus, the
// frame-need matrix, and the multi-file regression of E2 round 1 M1) is
// checked alone, then as file 0 and as file 1 of a two-file package beside
// neutralFile. Each placement must give exactly the one-file diagnostics,
// each naming the program's file, at the same line, column, offset and
// length, with the same code and message. A positive program must stay
// clean, so the neutral file adds no diagnostic of its own either.

// neutralFile declares a type, a package let, a function and a method-like
// use of each, under names no program uses, so every declaration pass
// visits it, and it interacts with no program.
const neutralFile = `type SplitNeutral { V int }

let splitNeutralZero = SplitNeutral{V: 0}

func splitNeutral(n SplitNeutral) int { n.V + splitNeutralZero.V }
`

// m1Program is E2 round 1 M1's reproduction: a context with in a package
// let, refused by checkFrames (S108 at 2:9).
const m1Program = "import log\nlet x = with log.Annotate(\"k\", \"v\") { 1 }\n"

// splitSources returns every program of the guard by name.
func splitSources(t testing.TB) map[string]string {
	out := corpus(t)
	for name, src := range check.FrameMatrixNegatives() {
		out["frame matrix: "+name] = src
	}
	out["E2 r1 M1"] = m1Program
	return out
}

func TestDiagnosticsStayInTheirFile(t *testing.T) {
	neutral, err := syntax.Parse(neutralFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ds := check.Check("main", []*syntax.File{neutral}, nil); len(ds) != 0 {
		t.Fatalf("the neutral file alone: %v", ds)
	}
	sources := splitSources(t)
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	negatives, frames := 0, 0
	for _, name := range names {
		src := sources[name]
		file, err := syntax.Parse(src)
		if err != nil {
			continue // a parse error is the parser's, reported before any pass
		}
		_, alone := check.Check("main", []*syntax.File{file}, nil)
		if len(alone) > 0 {
			negatives++
			if strings.HasPrefix(name, "frame matrix: ") || name == "E2 r1 M1" {
				frames++
			}
		}
		for at := range 2 {
			// Each check parses afresh, so no check sees another's tree.
			own, _ := syntax.Parse(src)
			other, _ := syntax.Parse(neutralFile)
			files := []*syntax.File{other, other}
			files[at] = own
			_, split := check.Check("main", files, nil)
			want := make([]check.Diagnostic, len(alone))
			for i, d := range alone {
				d.File = at
				want[i] = d
			}
			if len(split) != len(want) || len(want) > 0 && !reflect.DeepEqual(split, want) {
				t.Errorf("%s as file %d of two: the diagnostics move\n\talone: %s\n\tsplit: %s", name, at, placed(alone), placed(split))
			}
		}
	}
	t.Logf("%d programs, %d negative (%d frame-scope refusals), each checked as file 0 and file 1 of two", len(names), negatives, frames)
	if negatives < 150 || frames < 21 {
		t.Fatalf("the guard covers %d negative programs, %d frame-scope refusals; the corpus shrank", negatives, frames)
	}
}

// TestFrameRefusalNamesItsFile is E2 round 1 M1's regression as the review
// wrote it: the invalid initializer in each position of a two-file package
// whose other file is `func main() {}`, refused at file, 2:9.
func TestFrameRefusalNamesItsFile(t *testing.T) {
	for at := range 2 {
		texts := []string{"func main() {}\n", "func main() {}\n"}
		texts[at] = m1Program
		var files []*syntax.File
		for _, src := range texts {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		_, ds := check.Check("main", files, nil)
		if len(ds) != 1 || ds[0].Code != check.CodeType || ds[0].File != at || ds[0].Line != 2 || ds[0].Column != 9 ||
			ds[0].Offset+ds[0].Length > len(m1Program) ||
			m1Program[ds[0].Offset:ds[0].Offset+ds[0].Length] != `with log.Annotate("k", "v") { 1 }` {
			t.Errorf("the initializer as file %d: want one S108 at file %d, 2:9; got %s", at, at, placed(ds))
		}
	}
}

func placed(ds []check.Diagnostic) string {
	var parts []string
	for _, d := range ds {
		parts = append(parts, fmt.Sprintf("file %d %d:%d+%d %s %s", d.File, d.Line, d.Column, d.Length, d.Code, d.Message))
	}
	return strings.Join(parts, "; ")
}
