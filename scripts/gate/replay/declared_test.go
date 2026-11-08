package replay

import (
	"slices"
	"strings"
	"testing"
)

const declaredFixture = `package closure

var other = map[string][]allowance{"x": {{"no", file, "not the table"}}}

var declared = map[string][]allowance{
	"internal/residue": {
		{".", subtree, "reads every file"},
	},
	"internal/sketch/check": declare(
		glob("internal/sketch/testdata/*/*.ergo", "the corpus" + " one level down"),
		[]allowance{{"docs/spec/diagnostics.md", file, "the registry"}},
		glob("internal/sketch/testdata/*/*.ergo", "a repeat"),
	),
	"internal/testinputs": declare(
		exactly("no identity", "internal/testinputs/a.go", "internal/testinputs/b.go"),
	),
	"internal/sketch/lower": {
		{"cmd/ergo", program, "the CLI"},
		{"internal/sketch", listing, "walks"},
		{"examples/x/*", files, "copies"},
	},
	"scripts/conformance": {
		{".git", members, "git ls-files"},
	},
}
`

// ParseDeclared reads the table as internal/closure's helpers declare it:
// glob gives the listings and the file pattern, declare joins and drops
// repeats, exactly gives file entries; reasons are ignored.
func TestParseDeclared(t *testing.T) {
	table, err := ParseDeclared([]byte(declaredFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Allowance{
		"internal/residue": {{".", GrantSubtree}},
		"internal/sketch/check": {
			{"internal/sketch/testdata", GrantListing},
			{"internal/sketch/testdata/*", GrantListing},
			{"internal/sketch/testdata/*/*.ergo", GrantFiles},
			{"docs/spec/diagnostics.md", GrantFile},
		},
		"internal/testinputs":   {{"internal/testinputs/a.go", GrantFile}, {"internal/testinputs/b.go", GrantFile}},
		"internal/sketch/lower": {{"cmd/ergo", GrantProgram}, {"internal/sketch", GrantListing}, {"examples/x/*", GrantFiles}},
		"scripts/conformance":   {{".git", GrantMembers}},
	}
	if len(table) != len(want) {
		t.Fatalf("table has %d packages, want %d: %v", len(table), len(want), table)
	}
	for dir, allowances := range want {
		if !slices.Equal(table[dir], allowances) {
			t.Errorf("%s: %v, want %v", dir, table[dir], allowances)
		}
	}
}

// A form the parser does not know is an error, so no package is keyed from
// a table the gate cannot read.
func TestParseDeclaredRefusesUnknownForms(t *testing.T) {
	for name, entry := range map[string]string{
		"helper":  `widen("x", "why")`,
		"grant":   `{{"x", everything, "why"}}`,
		"keyed":   `{{path: "x", grant: file, reason: "why"}}`,
		"pattern": `declare(glob(prefix+"/x/*.ergo", "why"))`,
		"spread":  `declare(parts...)`,
	} {
		source := strings.Replace(declaredFixture, `{".", subtree, "reads every file"},`, "", 1)
		source = strings.Replace(source, `"internal/residue": {`, `"internal/residue": `+entry+`, "unused": {`, 1)
		if _, err := ParseDeclared([]byte(source)); err == nil {
			t.Errorf("%s: %s parsed", name, entry)
		}
	}
	if _, err := ParseDeclared([]byte("package closure\n")); err == nil {
		t.Error("a file without the table parsed")
	}
}
