package check

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// A member kind: a type declaration that holds n members of one name. Each
// of the two declarations of T has its own filler member (i is 1 or 2), so
// the two differ in more than the duplicate members.
type dupMemberKind struct {
	name   string // the member's name
	member string // the text of one member, the name in it
	decl   func(i, n int) string
	twice  string // the member duplicate message, "" when it is package-level
	extras []string
}

// later returns the offsets in decl of the member's name in each of its
// members after the first: where the kept declaration reports them.
func (k dupMemberKind) later(decl string) []int {
	var out []int
	for i, at := 0, 0; ; i++ {
		j := strings.Index(decl[at:], k.member)
		if j < 0 {
			return out
		}
		if i > 0 {
			out = append(out, at+j+strings.Index(k.member, k.name))
		}
		at += j + len(k.member)
	}
}

func offsetsOf(diags []Diagnostic) []int {
	var out []int
	for _, d := range diags {
		out = append(out, d.Offset)
	}
	return out
}

var dupMemberKinds = []dupMemberKind{
	{
		name:   "L",
		member: ", law L(){true}",
		decl: func(i, n int) string {
			return fmt.Sprintf("type T{a%d()int%s}\n", i, strings.Repeat(", law L(){true}", n))
		},
		twice: "law L is declared twice",
		// A top-level law may share its name with a member law (types.md,
		// Laws 2), so its duplicate is the package-level one.
		extras: []string{"law L() { true }\n", "law L() { true }\nlaw L() { true }\n"},
	},
	{
		name:   "m",
		member: ", m()int",
		decl: func(i, n int) string {
			return fmt.Sprintf("type T{a%d()int%s}\n", i, strings.Repeat(", m()int", n))
		},
		twice:  "method m is declared twice",
		extras: []string{"func m() int { 1 }\n"},
	},
	{
		name:   "x",
		member: ", x int",
		decl: func(i, n int) string {
			return fmt.Sprintf("type T{f%d int%s}\n", i, strings.Repeat(", x int", n))
		},
		twice:  "field x is declared twice",
		extras: []string{"func x() int { 1 }\n"},
	},
	{
		// An inline variant declares a record in the package scope (spec
		// D-B1), also in a refused declaration: its duplicate is
		// package-level.
		name:   "V",
		member: " | V{}",
		decl: func(i, n int) string {
			return fmt.Sprintf("type T = W%d{}%s\n", i, strings.Repeat(" | V{}", n))
		},
		extras: []string{"func V() int { 1 }\n"},
	},
}

// dupMemberDiags are the S108 diagnostics of a member kind's program:
// those of T's declaration, and those of its member (a top-level law's
// duplicate, which has a member law's message, is outside the types).
type dupMemberDiags struct {
	types, members []Diagnostic
}

func (k dupMemberKind) split(diags []Diagnostic, inTypes func(Diagnostic) bool) dupMemberDiags {
	var out dupMemberDiags
	for _, d := range diags {
		switch {
		case d.Message == "T is already declared as a type in this package":
			out.types = append(out.types, d)
		case k.twice != "" && d.Message == k.twice && inTypes(d),
			k.twice == "" && strings.HasPrefix(d.Message, k.name+" is already declared"):
			out.members = append(out.members, d)
		}
	}
	return out
}

// duplicateKeys are the verdict and the package-level duplicates of a
// package of files, as the declaration-order oracle compares them when the
// kept declaration depends on the order (duplicatesOf).
func duplicateKeys(files []*syntax.File, diags []Diagnostic) []string {
	var keys []string
	for _, d := range diags {
		if d.Code == CodeSyntax {
			continue
		}
		if name, ok := duplicateKey(d, files[d.File]); ok {
			keys = append(keys, fmt.Sprintf("%s duplicate %s", d.Code, name))
		}
	}
	slices.Sort(keys)
	return duplicatesOf(keys, len(diags) > 0)
}

func parseFiles(t *testing.T, srcs ...string) []*syntax.File {
	t.Helper()
	var files []*syntax.File
	for _, src := range srcs {
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("%v\n%s", err, src)
		}
		files = append(files, f)
	}
	return files
}

// TestDuplicateMembersInDuplicateTypes (fuzz-found-524f2c0c838cd56a): a type
// declared twice, each declaration with 0 to 2 members of one name (laws,
// methods, fields or variants), alone or beside package declarations named
// like the member, in one file and across two files in both orders.
//
// The rule: the first declaration of T is kept and the later one is S108,
// in the later file across files. The members of the kept declaration are
// checked, so each member after the first of its name is S108; the
// refused declaration is not checked (decl.go collect), except its inline
// variants, which are package-level. The oracle: neither the verdict nor
// the package-level duplicates depend on the order of declarations or of
// files, and a member law's duplicate is keyed by its type, apart from a
// top-level law of its name.
func TestDuplicateMembersInDuplicateTypes(t *testing.T) {
	programs := 0
	for _, k := range dupMemberKinds {
		for n1 := range 3 {
			for n2 := range 3 {
				for _, extra := range append([]string{""}, k.extras...) {
					name := fmt.Sprintf("%s/%d-%d/extra=%q", k.name, n1, n2, extra)
					d1, d2 := k.decl(1, n1), k.decl(2, n2)
					src := d1 + d2 + extra
					programs++

					// One file: the rule.
					f1 := parseFiles(t, src)
					_, one := Check("main", f1, nil)
					got := k.split(one, func(d Diagnostic) bool { return d.Offset < len(d1+d2) })
					if len(got.types) != 1 || got.types[0].Offset != len(d1)+len("type ") {
						t.Errorf("%s: want one type duplicate at the second T, got %v\n%s", name, got.types, src)
					}
					packageLevel := max(0, n1+n2-1+strings.Count(extra, "func "+k.name))
					if k.twice != "" {
						if have, want := offsetsOf(got.members), k.later(d1); !slices.Equal(have, want) || len(want) != max(0, n1-1) {
							t.Errorf("%s: member duplicates at %v, want %v in the kept declaration: %v\n%s", name, have, want, got.members, src)
						}
					} else if len(got.members) != packageLevel {
						t.Errorf("%s: want %d package-level duplicates of %s, got %v\n%s", name, packageLevel, k.name, got.members, src)
					}

					// One file: the declaration-order oracle.
					if p := OrderProblems(src, 6); len(p) > 0 {
						t.Errorf("%s: %s", name, p[0])
					}
					want := duplicateKeys(f1, one)

					// Two files, in both orders: the earlier file holds the
					// kept T, whose members report as in one file; the later
					// file holds the refused T; and the package-level
					// duplicates are those of one file.
					for _, order := range [][2]string{{d1 + extra, d2}, {d2, d1 + extra}} {
						files := parseFiles(t, order[0], order[1])
						_, two := Check("main", files, nil)
						// Each file starts with its T; a top-level law's
						// duplicate follows it.
						decls := [2]string{d1, d2}
						if order[0] == d2 {
							decls = [2]string{d2, d1}
						}
						got := k.split(two, func(d Diagnostic) bool { return d.Offset < len(decls[d.File]) })
						if len(got.types) != 1 || got.types[0].File != 1 || got.types[0].Offset != len("type ") {
							t.Errorf("%s files %q: want one type duplicate at T in file 1, got %v", name, order, got.types)
						}
						if k.twice != "" {
							want := k.later(decls[0])
							if have := offsetsOf(got.members); !slices.Equal(have, want) {
								t.Errorf("%s files %q: member duplicates at %v, want %v in file 0: %v", name, order, have, want, got.members)
							}
							for _, d := range got.members {
								if d.File != 0 {
									t.Errorf("%s files %q: a member duplicate outside the kept file: %v", name, order, d)
								}
							}
						} else if len(got.members) != packageLevel {
							t.Errorf("%s files %q: want %d package-level duplicates of %s, got %v", name, order, packageLevel, k.name, got.members)
						}
						if have := duplicateKeys(files, two); !slices.Equal(have, want) {
							t.Errorf("%s files %q: duplicates %v, one file %v", name, order, have, want)
						}
					}
				}
			}
		}
	}
	if programs != 4*9+5*9 {
		t.Fatalf("%d programs, want %d", programs, 4*9+5*9)
	}
}

// TestDuplicateKeyOwner: a member law's duplicate is keyed by its type,
// and a top-level law's by its name alone, though the two messages are
// the same, so a package-level law duplicate stays in the oracle's set.
func TestDuplicateKeyOwner(t *testing.T) {
	src := "type T{a()int, law L(){true}, law L(){true}}\nlaw L() { true }\nlaw L() { true }\n"
	f := parseFiles(t, src)[0]
	_, diags := Check("main", []*syntax.File{f}, nil)
	var keys []string
	for _, d := range diags {
		if k, ok := duplicateKey(d, f); ok {
			keys = append(keys, k)
		}
	}
	if !slices.Equal(keys, []string{"T.L", "L"}) {
		t.Fatalf("keys %v, want [T.L L] in %v", keys, diags)
	}
	if got := duplicatesOf([]string{"S108 duplicate T.L", "S108 duplicate L"}, true); !slices.Equal(got, []string{"rejected true", "L"}) {
		t.Fatalf("duplicatesOf: %v", got)
	}
}

// TestFuzzFoundDuplicateLawInDuplicateType is the saved input
// fuzz-found-524f2c0c838cd56a: A0000 is declared twice, and the second
// declaration holds four laws named A. Which declaration is kept depends
// on the order, and with it whether its laws report "law A is declared
// twice"; the oracle keys those by A0000 and finds no order problem.
func TestFuzzFoundDuplicateLawInDuplicateType(t *testing.T) {
	data, err := os.ReadFile("testdata/fuzz/FuzzCheck/fuzz-found-524f2c0c838cd56a")
	if err != nil {
		t.Fatal(err)
	}
	_, line, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	src, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "string("), ")"))
	if err != nil || !strings.HasPrefix(src, "type Malformed{}\ntype A0000{A00()string}") {
		t.Fatalf("decoding the saved input: %q", src)
	}
	lawDups := func(s string) int {
		_, diags := CheckSource(s)
		n := 0
		for _, d := range diags {
			if d.Message == "law A is declared twice" {
				n++
			}
		}
		return n
	}
	// The kept A0000 has no laws; with the two declarations swapped, the
	// kept one has four laws named A.
	lines := strings.Split(src, "\n")
	lines[1], lines[3] = lines[3], lines[1]
	if a, b := lawDups(src), lawDups(strings.Join(lines, "\n")); a != 0 || b != 3 {
		t.Fatalf("law duplicates: %d in the saved order, %d swapped; want 0 and 3", a, b)
	}
	if p := OrderProblems(src, 8); len(p) > 0 {
		t.Fatal(p[0])
	}
}
