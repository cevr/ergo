package syntax_test

// The parse laws of capstone S2e. Parse has no other parser to agree with
// since the Go parser was deleted; these laws hold it to the formatter,
// which prints every token the tree keeps (compiler/format, through
// internal/hostsyntax): what the formatter prints parses back to the same
// tree, and the repository's formatted files are what their trees print.

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/syntax"
)

// roundTrip is "" when the tree f of a source formats to text that parses
// to a tree equal to f but for positions (syntax.SameTree), and that text
// formats to itself; else what broke. out is the formatted text.
func roundTrip(f *syntax.File) (out, broke string) {
	out, err := format.FileBounded(f, 0)
	if err != nil {
		return "", "format: " + err.Error()
	}
	g, err := syntax.Parse(out)
	if err != nil {
		return out, "the formatted text does not parse: " + err.Error()
	}
	if d := syntax.SameTree(f, g); d != "" {
		return out, "the formatted text parses to another tree: " + d
	}
	again, err := format.FileBounded(g, 0)
	if err != nil {
		return out, "format again: " + err.Error()
	}
	if again != out {
		return out, "the formatted text formats to other text"
	}
	return out, ""
}

// sameWithCRLF is "" when src with every line ended by a carriage return
// and a newline formats to out, the text of src: a comment's and a raw
// string's text drop the carriage returns, as the line ends do.
func sameWithCRLF(src, out string) string {
	f, err := syntax.Parse(strings.ReplaceAll(src, "\n", "\r\n"))
	if err != nil {
		return "with CRLF line ends the source does not parse: " + err.Error()
	}
	crlf, err := format.FileBounded(f, 0)
	if err != nil {
		return "with CRLF line ends, format: " + err.Error()
	}
	if crlf != out {
		return "with CRLF line ends the source formats to other text\n" + firstLineDiff(out, crlf)
	}
	return ""
}

// TestFormatParseRoundTrip: for each seeded generated program that parses
// (parse_gen_test.go writes every decided form, with stray tokens now and
// then), parse(format(t)) is t but for positions, format is a fixpoint of
// its own text, and the program with CRLF line ends formats to the same
// text. A failing seed prints the source, the formatted text,
// what broke and the replay command.
func TestFormatParseRoundTrip(t *testing.T) {
	parsed, failures := 0, 0
	for _, in := range syntax.GeneratedSources(t) {
		f, err := syntax.Parse(in.Src)
		if err != nil {
			continue
		}
		parsed++
		out, broke := roundTrip(f)
		if broke == "" {
			broke = sameWithCRLF(in.Src, out)
		}
		if broke == "" {
			continue
		}
		failures++
		if failures <= 3 {
			t.Errorf("%s: %s\nsource %q\nformatted %q%s", in.Name, broke, in.Src, out, syntax.Replay(in, "TestFormatParseRoundTrip"))
		}
	}
	if parsed == 0 {
		t.Fatal("no generated program parses")
	}
	if failures > 0 {
		t.Errorf("%d of %d parsed programs break the round trip", failures, parsed)
	}
}

// formattedRoots are the folders whose .ergo files the repository keeps
// formatted (`ergo fmt --check` of the gate and of compiler/'s lane).
var formattedRoots = []string{"compiler", "examples"}

// TestFormatIsTheCorpusFixpoint: every file of the corpus that parses
// round-trips (as in TestFormatParseRoundTrip), and a file under
// formattedRoots is exactly what its tree formats to, so a token the parser
// drops or invents is a difference in the text.
func TestFormatIsTheCorpusFixpoint(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	failures, kept := 0, 0
	for _, in := range syntax.CorpusSources(t) {
		f, err := syntax.Parse(in.Src)
		if err != nil {
			continue
		}
		out, broke := roundTrip(f)
		if broke == "" && formatted(root, in.Name) {
			kept++
			if out != in.Src {
				broke = "a formatted file formats to other text"
			}
		}
		if broke == "" {
			continue
		}
		failures++
		if failures <= 3 {
			t.Errorf("%s: %s\n%s", in.Name, broke, firstLineDiff(in.Src, out))
		}
	}
	if kept == 0 {
		t.Fatal("no file of formattedRoots was read")
	}
	if failures > 0 {
		t.Errorf("%d corpus files break the fixpoint", failures)
	}
}

func formatted(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	if err != nil {
		return false
	}
	for _, r := range formattedRoots {
		if strings.HasPrefix(rel, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// firstLineDiff is the first line where a and b differ, with its number.
func firstLineDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return "line " + strconv.Itoa(i+1) + ":\n- " + x + "\n+ " + y
		}
	}
	return ""
}
