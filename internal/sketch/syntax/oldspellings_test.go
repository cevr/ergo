package syntax_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/sketchtest"
	"ergo.local/prototype/internal/sketch/syntax"
)

const grammarPath = "../../../docs/spec/grammar.md"

// oldSpellingsList matches grammar.md's list of old Ergo-only spellings and
// the code it states for them.
var oldSpellingsList = regexp.MustCompile(`\*\*Old Ergo-only spellings\*\* \(([^\n]*?)\) get the plain (S\d{3})`)

var codeSpan = regexp.MustCompile("`([^`]+)`")

// TestOldSpellingsHaveFixtures (196w-f1): every spelling in backticks in
// grammar.md's "Old Ergo-only spellings" list appears verbatim in a refuse
// fixture or a sketchtest.MalformedTable row that expects the code the list
// states. TestRefusedFixtures and TestMalformed prove that each such input
// gets that code; this test proves that the list and the inputs agree, so a
// rename pass over the inputs cannot leave a listed spelling untested. In a
// spelling, … stands for any text and a run of white space for any run.
func TestOldSpellingsHaveFixtures(t *testing.T) {
	data, err := os.ReadFile(grammarPath)
	if err != nil {
		t.Fatal(err)
	}
	m := oldSpellingsList.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("%s: no old Ergo-only spellings list", grammarPath)
	}
	spellings, code := codeSpan.FindAllStringSubmatch(m[1], -1), m[2]
	if len(spellings) == 0 {
		t.Fatalf("%s: the old spellings list has no spelling in backticks", grammarPath)
	}
	type input struct{ name, code, src string }
	var inputs []input
	for _, in := range readDir(t, "refuse") {
		head, body, _ := strings.Cut(in.Src, "\n")
		want, _, _ := strings.Cut(strings.TrimPrefix(head, "// want: "), " ")
		inputs = append(inputs, input{in.Name, want, body})
	}
	for _, r := range sketchtest.MalformedTable {
		want := r.Code
		if want == "" {
			want = syntax.CodeSyntax
		}
		inputs = append(inputs, input{"sketchtest " + r.Name, want, r.Src})
	}
	for _, s := range spellings {
		re := spellingPattern(s[1])
		found := false
		for _, in := range inputs {
			if in.code == code && re.MatchString(in.src) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("old spelling `%s` (%s): no refuse fixture or malformed row expecting %s holds it", s[1], grammarPath, code)
		}
	}
}

// spellingPattern compiles a spelling of the list: … matches any text, a run
// of white space any run, and a spelling that starts or ends with a word
// character matches only at a word boundary.
func spellingPattern(spelling string) *regexp.Regexp {
	var pieces []string
	for _, piece := range strings.Split(spelling, "…") {
		words := strings.Fields(piece)
		for i, w := range words {
			words[i] = regexp.QuoteMeta(w)
		}
		if len(words) > 0 {
			pieces = append(pieces, strings.Join(words, `\s+`))
		}
	}
	expr := strings.Join(pieces, `(?s:.*?)`)
	if regexp.MustCompile(`^\w`).MatchString(spelling) {
		expr = `\b` + expr
	}
	if regexp.MustCompile(`\w$`).MatchString(spelling) {
		expr += `\b`
	}
	return regexp.MustCompile(expr)
}
