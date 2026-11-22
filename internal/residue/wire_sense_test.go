package residue_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The word wire has one sense in Ergo now: a codec's wire form
// (GLOSSARY.md, Codec: ParseX decodes and x.Wire() encodes), and with it
// "on the wire", a wire format, the wire key of a field, and the Wire
// transports of an Api. The construct that binds capabilities for a block
// was spelled wire until 196w and is spelled context
// (docs/previews/2026-11-02-wire-to-context.md: "The network sense
// stays"). So the old sense is the construct's syntax, and these tests
// look for that syntax, not for the word: wire is a keyword nowhere
// (check's TestWireIsAnIdentifier, the refuse fixtures 49-wire-let and
// 50-wire-with), every checked program refuses `wire { … }` as an unknown
// type, and what stays is the code a document shows and the glossary's
// terms.

// wireHistory are the dated records that keep the words of their day
// (196w decision 6): receipts, the dated previews, the plans, the frozen
// prototypes and upstream copies, the research notes.
var wireHistory = []string{
	"docs/receipts/", "docs/previews/2026-", "plans/", "prototypes/sketch9/",
	"conformance/upstream/", "docs/research/",
}

// wireConstruct matches the token text (syntax.TokenText) of the context
// construct in wire's old place: `with wire`, `= wire {`, and a header
// `wire provides` or `wire needs`.
var wireConstruct = regexp.MustCompile(`(?m)^with "with"\nIDENT "wire"\n|^= "="\nIDENT "wire"\n\{ "\{"\n|^IDENT "wire"\n(provides "provides"|needs "needs")\n`)

// TestDocsShowNoWireConstruct: a fenced block of a tracked Markdown file
// outside history, read by Ergo's own scanner, never spells the context
// construct as wire. The checker refuses it in a program; a document's
// code is checked by nothing else.
func TestDocsShowNoWireConstruct(t *testing.T) {
	repo := wireRepo(t)
	out, err := exec.Command("git", "-C", repo, "--no-optional-locks", "ls-files", "-z", "--", "*.md").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if path == "" || wireIsHistory(path) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range fencedBlocks(string(data)) {
			if at := wireConstruct.FindString(syntax.TokenText(block.text)); at != "" {
				t.Errorf("%s:%d: the block spells the context construct as wire (%q); write context", path, block.line, strings.ReplaceAll(at, "\n", " "))
			}
		}
	}
}

// TestWireConstructTokens: the construct's three old forms are found, and
// the codec's and the transport's wire are not (the mutants: a pattern
// that misses a form, or one that reads the word).
func TestWireConstructTokens(t *testing.T) {
	for _, src := range []string{
		"with wire { Store: Mem{} } { f() }\n",
		"let App = wire { Store: Mem{} }\n",
		"let App = wire provides Account needs Config { Store: Mem{} }\n",
	} {
		if !wireConstruct.MatchString(syntax.TokenText(src)) {
			t.Errorf("%q: the old construct is not found", src)
		}
	}
	for _, src := range []string{
		"func Wire(x Port) int { x.N }\n",
		"let b = p.Wire([]byte{})\n",
		"with context { Store: Mem{} } { send(wire) }\n",
		"let wire = 1\n",
		"type Wire = Request\n",
	} {
		if at := wireConstruct.FindString(syntax.TokenText(src)); at != "" {
			t.Errorf("%q: found %q, which is not the construct", src, at)
		}
	}
}

// TestGlossaryHasNoWireConstruct: the glossary defines no term for the
// construct under its old name (Wire, Wire exposure, Inline wire, Named
// wire, Closed wire); the word's sense is Codec's.
func TestGlossaryHasNoWireConstruct(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(wireRepo(t), "GLOSSARY.md"))
	if err != nil {
		t.Fatal(err)
	}
	term := regexp.MustCompile(`^\*\*([^*]+)\*\*`)
	word := regexp.MustCompile(`(?i)\bwires?\b`)
	for n, line := range strings.Split(string(data), "\n") {
		if m := term.FindStringSubmatch(line); m != nil && word.MatchString(m[1]) {
			t.Errorf("GLOSSARY.md:%d: the term %q names the construct by its old word; it is context", n+1, m[1])
		}
	}
}

type fenced struct {
	line int
	text string
}

// fencedBlocks are the ``` and ~~~ blocks of a Markdown text, each with the
// line of its opening fence.
func fencedBlocks(md string) []fenced {
	var blocks []fenced
	var cur *fenced
	var fence string
	for n, line := range strings.Split(md, "\n") {
		trimmed := strings.TrimSpace(line)
		if cur == nil {
			for _, f := range []string{"```", "~~~"} {
				if strings.HasPrefix(trimmed, f) {
					cur, fence = &fenced{line: n + 1}, f
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, fence) {
			blocks = append(blocks, *cur)
			cur = nil
			continue
		}
		cur.text += line + "\n"
	}
	return blocks
}

func wireIsHistory(path string) bool {
	for _, h := range wireHistory {
		if strings.HasPrefix(path, h) {
			return true
		}
	}
	return false
}

func wireRepo(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(root))
}
