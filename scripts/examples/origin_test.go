package main

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOriginFollowsUpstream is a seeded property of the origin folder's
// check against a reference: each seed writes upstream story and scene
// files of drawn tests (every call form: test(, it(, it.effect(, their
// .each tables, titles across lines, and calls that are not tests:
// init(, x.it(, and tests in a file that is not a story or a scene) and
// a tests.txt with one line per drawn test. That tests.txt passes; each
// edit of it fails, naming the line: one line dropped, one doubled, one
// that upstream lacks, one with no restatement. A failing seed prints
// the files and the replay command.
//
// Mutants it kills (scripts/examples' ledger has none yet; named here and
// in the FK0 request): an .each table read as the title; it.effect not a
// test; init( read as a test; a title across lines not collapsed; a file
// that is not a story or scene read; an extra line accepted; a doubled
// line accepted; a line with no restatement accepted. An odd seed draws
// two upstream folders (FKsrc), killing: only source's first line read;
// FILE not prefixed with its folder's name; two folders of one name
// accepted.
func TestOriginFollowsUpstream(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 60; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		root := t.TempDir()
		// An even seed has one upstream folder; an odd seed has two, whose
		// FILEs start with the folder's last element.
		folders := map[string]string{"up": ""}
		if seed%2 == 1 {
			folders = map[string]string{"up/a": "a/", "vendor/b": "b/"}
		}
		files := map[string]string{}
		var lines []string
		var source []string
		for _, folder := range slices.Sorted(maps.Keys(folders)) {
			drawn, ls := drawUpstream(r)
			for name, src := range drawn {
				files[folder+"/"+name] = src
			}
			for _, l := range ls {
				lines = append(lines, folders[folder]+l)
			}
			source = append(source, folder)
		}
		for name, src := range files {
			writeFile(t, filepath.Join(root, name), src)
		}
		origin := filepath.Join(root, "examples", "p", "origin")
		writeFile(t, filepath.Join(origin, "README.md"), "# origin\n")
		writeFile(t, filepath.Join(origin, "source"), strings.Join(source, "\n")+"\n")
		fail := func(what string, problems []string) {
			t.Helper()
			var b strings.Builder
			for name, src := range files {
				fmt.Fprintf(&b, "--- %s\n%s\n", name, src)
			}
			t.Fatalf("seed %d: %s: %q\n%s\nreplay: go test ./scripts/examples -run TestOriginFollowsUpstream", seed, what, problems, b.String())
		}
		check := func(ls []string) []string {
			writeFile(t, filepath.Join(origin, "tests.txt"), "# comment\n"+strings.Join(ls, "\n")+"\n")
			return checkOrigin(origin, root)
		}
		if problems := check(lines); len(problems) != 0 {
			fail("the drawn tests.txt is refused", problems)
		}
		if len(source) > 1 {
			// Two folders whose last elements match would give FILEs that
			// cannot be told apart.
			writeFile(t, filepath.Join(origin, "source"), "up/a\nvendor/b\nvendor/a\n")
			problems := checkOrigin(origin, root)
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, "same folder name") }) {
				fail("two folders of one name are not named", problems)
			}
			writeFile(t, filepath.Join(origin, "source"), strings.Join(source, "\n")+"\n")
		}
		k := r.IntN(len(lines))
		key := lines[k][:strings.LastIndex(lines[k], " -> ")]
		edits := map[string][]string{
			"a line dropped":        append(append([]string{}, lines[:k]...), lines[k+1:]...),
			"a line doubled":        append(append([]string{}, lines...), lines[k]),
			"a line upstream lacks": append(append([]string{}, lines...), "story.test.ts: no such test -> x"),
			"no restatement":        append(append(append([]string{}, lines[:k]...), key+" -> "), lines[k+1:]...),
		}
		for what, ls := range edits {
			problems := check(ls)
			named := false
			for _, p := range problems {
				named = named || strings.Contains(p, key) || strings.Contains(p, "no such test") || strings.Contains(p, "no restatement")
			}
			if len(problems) == 0 || !named {
				fail(what+" is not named", problems)
			}
		}
	}
}

// Both layouts of a program, one folder and a program of packages, check an
// origin folder's tests.txt, and neither wants a record.sh beside it.
// Mutants: either layout's call of checkOrigin removed.
func TestOriginFolderIsChecked(t *testing.T) {
	t.Parallel()
	layouts := map[string]map[string]string{
		"single": {"main.ergo": "func main() {}\n"},
		"module": {"ergo.mod": "module m\n", "cmd/module/main.ergo": "func main() {}\n"},
	}
	for name, files := range layouts {
		for _, complete := range []bool{true, false} {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "up", "src", "story.test.ts"), "test('one', () => {})\ntest('two', () => {})\n")
			dir := filepath.Join(root, "examples", name)
			for file, content := range files {
				writeFile(t, filepath.Join(dir, file), content)
			}
			writeFile(t, filepath.Join(dir, "README.md"), "# "+name+"\n")
			writeFile(t, filepath.Join(dir, "tags"), "default\n")
			writeFile(t, filepath.Join(dir, "cases", "basic", "expected.out"), "")
			writeFile(t, filepath.Join(dir, "origin", "README.md"), "# origin\n")
			writeFile(t, filepath.Join(dir, "origin", "source"), "up\n")
			tests := "src/story.test.ts: one -> T1\n"
			if complete {
				tests += "src/story.test.ts: two -> not restated: a reason\n"
			}
			writeFile(t, filepath.Join(dir, "origin", "tests.txt"), tests)
			_, problems := loadProgram(dir, name, false)
			missing := false
			for _, p := range problems {
				missing = missing || strings.Contains(p, "no line for src/story.test.ts: two")
				if strings.Contains(p, "record.sh") || strings.Contains(p, "not a regular file") {
					t.Errorf("%s: an origin folder is refused: %q", name, problems)
				}
			}
			if missing == complete {
				t.Errorf("%s, complete %v: want the missing title named exactly when it is missing; %q", name, complete, problems)
			}
		}
	}
}

// drawUpstream draws upstream test files and the tests.txt lines that
// restate them.
func drawUpstream(r *rand.Rand) (map[string]string, []string) {
	words := []string{"start", "the", "clock", "a", "(paren)", "[x]", "$name", "%s", "stops", "it", "test", "->"}
	files := map[string]string{}
	var lines []string
	names := []string{"story.test.ts", "src/scene.test.ts", "story/app.story.test.ts"}
	for _, name := range names {
		var b strings.Builder
		b.WriteString("const init = (x) => x\nconst subject = { it: (s) => s }\n")
		for n := 1 + r.IntN(5); n > 0; n-- {
			var ws []string
			for k := 1 + r.IntN(5); k > 0; k-- {
				ws = append(ws, words[r.IntN(len(words))])
			}
			title := strings.Join(ws, " ")
			// Each title is distinct within a file: a drawn title gets the
			// line count as a suffix.
			title += fmt.Sprintf(" %d", len(lines))
			quote := []string{"'", "\"", "`"}[r.IntN(3)]
			split := strings.Replace(title, " ", "\n    ", 1)
			switch r.IntN(6) {
			case 0:
				fmt.Fprintf(&b, "test(%s%s%s, () => {\n  init(%s)\n})\n", quote, title, quote, quote+"not a title"+quote)
			case 1:
				fmt.Fprintf(&b, "  it(%s%s%s, () => subject.it('not a title'))\n", quote, title, quote)
			case 2:
				fmt.Fprintf(&b, "it.effect(\n  %s%s%s,\n  () => Effect.void,\n)\n", quote, split, quote)
			case 3:
				fmt.Fprintf(&b, "it.effect.each([\n  ['a)', 1],\n  [\"b]\", 2],\n])(%s%s%s, ([s, n]) => Effect.void)\n", quote, title, quote)
			case 4:
				fmt.Fprintf(&b, "test.each([1, 2])(\n  %s%s%s,\n  (n) => {},\n)\n", quote, title, quote)
			default:
				fmt.Fprintf(&b, "describe('group', () => {\n  it.only(%s%s%s, () => {})\n})\n", quote, title, quote)
			}
			lines = append(lines, fmt.Sprintf("%s: %s -> restated by T%d", name, title, len(lines)))
		}
		files[name] = b.String()
	}
	files["helpers.ts"] = "test('a helper file is not a story', () => {})\n"
	return files, lines
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
