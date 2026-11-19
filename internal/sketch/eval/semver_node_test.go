package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// semverProbe calls std semver as a program would: a text that is not a
// version or a range reads as node-semver's null.
const semverProbe = `import semver

func valid(s string) string {
	let v = semver.Parse(s) catch {
	case semver.Malformed:
		return "null"
	}
	semver.Format(v)
}

func validRange(s string) string {
	let r = semver.ParseRange(s) catch {
	case semver.Malformed:
		return "null"
	}
	semver.FormatRange(r)
}

func satisfies(r string, v string) bool {
	let rs = semver.ParseRange(r) catch {
	case semver.Malformed:
		return false
	}
	let vs = semver.Parse(v) catch {
	case semver.Malformed:
		return false
	}
	semver.Satisfies(rs, vs)
}

func compare(a string, b string) int {
	let x = semver.Parse(a) catch {
	case semver.Malformed:
		return 9
	}
	let y = semver.Parse(b) catch {
	case semver.Malformed:
		return 9
	}
	semver.Compare(x, y)
}

test T {
	assert true
}
`

// TestSemverIsNode is the property SemverIsNode (stdlib.md, semver): std
// semver gives node-semver 7.8.5's answer on every row of
// testdata/semver/node.jsonl, which testdata/semver/record.ts generates
// from node-semver's grammar with near misses (leading zeros, parts past
// 2^53-1, partial versions, x-ranges, hyphen ranges, stray operators and
// white space, build metadata) and records from node: semver.valid,
// semver.validRange, semver.satisfies and semver.compare. A failing row
// prints the call and both answers.
//
// Mutants only it kills (internal/sketch/stdlib/testdata/mutants.json):
// every sq4- mutant but sq4-word-prefix-equal.
func TestSemverIsNode(t *testing.T) {
	pkg := checked(t, semverProbe)
	funcs := map[string]*check.Func{}
	for _, f := range pkg.Funcs {
		funcs[f.Name] = f
	}
	in := &interp{budget: DefaultBudget(), root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, host: &host{}}
	if f := in.initialize(pkg); f != nil {
		t.Fatalf("initializers: %s", f.reason)
	}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	call := func(name string, args ...Value) Value {
		in.steps, in.bytes = 0, 0
		v, ab := in.call(fr, nil, funcs[name], nil, args)
		if ab != nil {
			t.Fatalf("%s%q: %v", name, args, ab)
		}
		return v
	}
	f, err := os.Open("testdata/semver/node.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows := 0
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		var row []any
		if err := json.Unmarshal(lines.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		var got, want any
		switch row[0] {
		case "valid":
			got, want = call("valid", row[1].(string)), row[2]
			if want == nil {
				want = "null"
			}
		case "range":
			got, want = call("validRange", row[1].(string)), row[2]
			if want == nil {
				want = "null"
			}
		case "satisfies":
			got, want = call("satisfies", row[1].(string), row[2].(string)), row[3]
		case "compare":
			got, want = call("compare", row[1].(string), row[2].(string)), int64(row[3].(float64))
		default:
			t.Fatalf("unknown row %s", lines.Text())
		}
		rows++
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: std semver gives %v, node-semver %v", lines.Text(), got, want)
		}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Fatal("no rows")
	}
}
