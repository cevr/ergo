package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// pieces are the declarations the generator puts together: every kind
// the Splitter keys (methods on pointer and generic receivers, groups,
// tables, structs, interfaces, init, blank vars), with comments above,
// after and inside.
var pieces = []string{
	"import \"fmt\"\n",
	"import (\n\t\"os\" // files\n\tstr \"strings\"\n)\n",
	"// F is a function.\nfunc F() int { return 1 }\n",
	"func g(a, b int) int {\n\t// inside\n\treturn a + b\n} // after g\n",
	"type T struct {\n\tA int // the a\n\tb, c string\n\tfmt.Stringer\n}\n",
	"func (t *T) M() {}\n",
	"func (t T) N() string { return t.b }\n",
	"type G[P any] struct{ v P }\n",
	"func (g *G[P]) Get() P { return g.v }\n",
	"type I interface {\n\tM()\n\tN() string\n}\n",
	"const (\n\tA = iota\n\t// B is two.\n\tB\n\tC, D = 5, 6\n)\n",
	"var table = []struct{ in, out string }{\n\t{\"a\", \"b\"},\n\t{\"c\", \"d\"}, // second\n}\n",
	"var byName = map[string]int{\n\t\"one\": 1,\n\t\"two\": 2,\n}\n",
	"func init() {}\n",
	"var _ = F\n",
	"/* a block\n   comment */\nvar x, y = 1, 2\n",
	"type (\n\tU int\n\tV = U\n)\n",
}

// generated is a Go file of the seed's pieces, in its order, imports
// first, with blank lines and comments between.
func generated(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var b strings.Builder
	if r.IntN(2) == 0 {
		b.WriteString("// Package p is generated.\n")
	}
	b.WriteString("package p\n")
	n := 1 + r.IntN(len(pieces))
	order := r.Perm(len(pieces))[:n]
	var imports, rest []string
	for _, i := range order {
		if strings.HasPrefix(pieces[i], "import") {
			imports = append(imports, pieces[i])
		} else {
			rest = append(rest, pieces[i])
		}
	}
	for _, p := range append(imports, rest...) {
		switch r.IntN(4) {
		case 0:
			b.WriteString("\n")
		case 1:
			b.WriteString("\n// a note " + strconv.Itoa(r.IntN(9)) + "\n")
		}
		b.WriteString(p)
	}
	if r.IntN(3) == 0 {
		b.WriteString("\n// the end\n")
	}
	return b.String()
}

// corpus is the generated files of seeds 1..n and this package's own
// source files.
func corpus(t *testing.T, n int) map[string]string {
	t.Helper()
	files := map[string]string{}
	for s := 1; s <= n; s++ {
		files[fmt.Sprintf("gen%d.go", s)] = generated(uint64(s))
	}
	for _, f := range []string{"split.go", "main.go", "split_test.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		files[f] = string(b)
	}
	return files
}

func joined(us []Unit) string {
	var b strings.Builder
	for _, u := range us {
		b.WriteString(u.Text)
	}
	return b.String()
}

// TestSplitterLaws holds the Splitter laws of modules/work/work/language.ergo
// on the corpus: UnitsAreTheFile, KeysAreUnique, members one run of their
// unit's text with unique keys, and EditStaysInItsUnit for a space added
// after each space of the file (the edit work plugin-test makes). A
// failing file prints its name; a generated one replays with its seed
// (generated(N)).
func TestSplitterLaws(t *testing.T) {
	for name, text := range corpus(t, 300) {
		us, err := Units(name, text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lawsHold(t, name, text, us, -1)
	}
}

// TestSplitterLawsOnThisRepository holds the same laws on every Go file of
// this repository (plan-r3 M1), EditStaysInItsUnit at the first space of
// each unit: a file go/parser cannot read (a test's broken input) is
// Unreadable, never a wrong cut.
func TestSplitterLawsOnThisRepository(t *testing.T) {
	read, unreadable := 0, 0
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "../.." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		us, err := Units(path, string(b))
		if err != nil {
			unreadable++
			return nil
		}
		read++
		lawsHold(t, path, string(b), us, 1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if read < 100 {
		t.Fatalf("read %d Go files of the repository (%d unreadable): not the repository", read, unreadable)
	}
	t.Logf("%d Go files hold the laws, %d unreadable", read, unreadable)
}

// lawsHold checks the Splitter laws on one file's units us: at most edits
// spaces added in each unit (every one when edits < 0).
func lawsHold(t *testing.T, name, text string, us []Unit, edits int) {
	t.Helper()
	{
		if joined(us) != text {
			t.Fatalf("%s: UnitsAreTheFile: the units join to\n%s", name, joined(us))
		}
		keys := map[string]bool{}
		for _, u := range us {
			if keys[u.Key] {
				t.Fatalf("%s: KeysAreUnique: %q twice", name, u.Key)
			}
			keys[u.Key] = true
			if len(u.Members) > 0 && !strings.Contains(u.Text, joined(u.Members)) {
				t.Fatalf("%s: the members of %q are not one run of its text", name, u.Key)
			}
			mk := map[string]bool{}
			for _, m := range u.Members {
				if mk[m.Key] {
					t.Fatalf("%s: member %q of %q twice", name, m.Key, u.Key)
				}
				mk[m.Key] = true
			}
		}
		// With edits >= 0, eight units of the file spread over it are
		// edited, edits spaces each.
		step := 1
		if edits >= 0 && len(us) > 8 {
			step = len(us) / 8
		}
		start := 0
		for i, u := range us {
			if i%step != 0 {
				start += len(u.Text)
				continue
			}
			made := 0
			for at := start + 1; at < start+len(u.Text) && (edits < 0 || made < edits); at++ {
				if text[at-1] != ' ' {
					continue
				}
				made++
				edited := text[:at] + " " + text[at:]
				after, err := Units(name, edited)
				if err != nil || len(after) != len(us) {
					continue
				}
				for j := range us {
					if after[j].Key != us[j].Key || (j != i && after[j].Text != us[j].Text) {
						t.Fatalf("%s: EditStaysInItsUnit: a space at %d in %q moved %q", name, at, u.Key, us[j].Key)
					}
				}
			}
			start += len(u.Text)
		}
	}
}

// TestKeysAreNames: the smallest file of each key rule. A method is keyed
// with its receiver's type (pointer and type parameters dropped), the
// comment above a declaration is in its unit and the one after it on its
// line too, imports are members keyed by path, a struct's fields and a
// table's elements are members, and a second init is "init #2".
func TestKeysAreNames(t *testing.T) {
	text := "package p\n\nimport (\n\t\"os\"\n\tx \"strings\"\n)\n\n// M doc.\nfunc (g *G[P]) M() {} // after\n\ntype S struct {\n\tA int\n\tB string\n}\n\nvar rows = []int{\n\t1,\n\t2,\n}\n\nfunc init() {}\n\nfunc init() {}\n"
	us, err := Units("p.go", text)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range us {
		var ms []string
		for _, m := range u.Members {
			ms = append(ms, m.Key)
		}
		got = append(got, u.Key+"["+strings.Join(ms, " ")+"]")
	}
	want := `package[] import["os" "strings"] p.G.M[] p.S[A B] p.rows[1 2] p.init[] p.init #2[]`
	if strings.Join(got, " ") != want {
		t.Fatalf("keys\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if !strings.HasPrefix(us[2].Text, "\n// M doc.\nfunc") || !strings.HasSuffix(us[2].Text, "// after\n") {
		t.Fatalf("the method's unit is %q", us[2].Text)
	}
}

// TestProtocol: a UnitsOf line is answered UnitsAre, a file that does not
// parse Unreadable, another request NotProvided, a line that is no
// request Unanswered: one answer a line, in order.
func TestProtocol(t *testing.T) {
	in := `{"_tag":"UnitsOf","file":"a.go","text":"package a\n"}` + "\n" +
		`{"_tag":"UnitsOf","file":"b.go","text":"package"}` + "\n" +
		`{"_tag":"CommentOf","text":"x"}` + "\n" + "nonsense\n"
	var out bytes.Buffer
	if err := serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	tags := []string{`"_tag":"UnitsAre"`, `"_tag":"Unreadable"`, `"_tag":"NotProvided"`, `"_tag":"Unanswered"`}
	if len(lines) != len(tags) {
		t.Fatalf("answers %q", lines)
	}
	for i, tag := range tags {
		if !strings.Contains(lines[i], tag) {
			t.Fatalf("answer %d is %s, want %s", i, lines[i], tag)
		}
	}
	if !strings.Contains(lines[2], `"op":"Comment"`) {
		t.Fatalf("NotProvided names no op: %s", lines[2])
	}
}
