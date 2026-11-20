package stdlib_test

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdLawSources is the source text of every std law with a source slice,
// from its name to its closing brace with its lines as written, by ledger
// key. A derived or by-name pair law has none (lawStatement).
func stdLawSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(key string, f *syntax.File, d *syntax.LawDecl) {
		if d.Derived == nil && d.Pair == nil {
			out[key] = f.Src[d.Name.Pos():d.Body.End()]
		}
	}
	for _, p := range stdLawPackages(t) {
		for _, f := range p.files {
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *syntax.LawDecl:
					add(p.path+" "+d.Name.Name, f, d)
				case *syntax.TypeDecl:
					if it, ok := d.Spec.(*syntax.InterfaceType); ok {
						for _, l := range it.Laws {
							add(p.path+" "+d.Name.Name+"."+l.Name.Name, f, l)
						}
					}
				}
			}
		}
	}
	return out
}

// literal is a string, rune or raw string literal of a law's text.
var literal = regexp.MustCompile("\"(?:\\\\.|[^\"\\\\])*\"|'(?:\\\\.|[^'\\\\])*'|`[^`]*`")

// outsideLiterals is s with edit applied to each stretch outside its
// literals.
func outsideLiterals(s string, edit func(string) string) string {
	var b strings.Builder
	at := 0
	for _, m := range literal.FindAllStringIndex(s, -1) {
		b.WriteString(edit(s[at:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		at = m[1]
	}
	b.WriteString(edit(s[at:]))
	return b.String()
}

// unformat respaces a law's text as a writer might before ergo fmt: a
// space after each ( { and comma and before each ) and }, and none around
// == != && || and binary +.
var unformat = strings.NewReplacer(
	"(", "( ", "{", "{ ", ",", " , ", ")", " )", "}", " }",
	" == ", "==", " != ", "!=", " && ", "&&", " || ", "||", " + ", "+",
)

// TestLawledgerSameIsTheFormatters is lawledger.Same's licence: Same is
// not a second definition of "the same law", but the formatter's, as far
// as a statement can be compared without importing the formatter (which
// runtime/v1's runners could not: hostsyntax imports runtime/v1). Over
// every std law with source text, respaced as above: the formatter
// prints the respaced law as it prints the source, and Same holds the
// source's statement the same as the respaced law and as the formatter's
// output. Over seeded single-token edits of each statement (a name, an
// operator, a number, a space inside a string) Same holds them different.
// A failing law prints its key and the edit; the seed is fixed (1).
//
// Mutant only it kills (internal/lawledger/testdata/mutants.json):
// same-brackets-are-operators.
func TestLawledgerSameIsTheFormatters(t *testing.T) {
	sources := stdLawSources(t)
	if len(sources) == 0 {
		t.Fatal("no std law with source text")
	}
	r := rand.New(rand.NewPCG(1, 0))
	name := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	number := regexp.MustCompile(`\b[0-9]+\b`)
	operator := regexp.MustCompile(` (==|!=|&&|\|\||\+|<|>) `)
	swap := map[string]string{"==": "!=", "!=": "==", "&&": "||", "||": "&&", "+": "-", "<": ">", ">": "<"}
	for _, key := range sortedKeys(sources) {
		src := sources[key]
		statement := lawledger.Normalize(src)
		respaced := outsideLiterals(src, unformat.Replace)
		format := func(text string) (string, bool) {
			out, err := hostsyntax.Source("law " + text + "\n")
			if err != nil {
				t.Errorf("%s: the formatter refuses the law %q: %v", key, text, err)
				return "", false
			}
			return lawledger.Normalize(strings.TrimPrefix(out, "law ")), true
		}
		fromSource, ok1 := format(src)
		fromRespaced, ok2 := format(respaced)
		if !ok1 || !ok2 {
			continue
		}
		if fromSource != fromRespaced {
			t.Errorf("%s: the respacing is not the formatter's to undo:\n\t%s\n\t%s", key, fromSource, fromRespaced)
			continue
		}
		// The formatter equates the source, the respaced law and its
		// output; so must Same.
		for _, text := range []string{respaced, fromSource} {
			if !lawledger.Same(statement, text) {
				t.Errorf("%s: Same refuses a text the formatter equates with the law:\n\t%s\n\t%s", key, statement, lawledger.Normalize(text))
			}
		}
		pick := func(at [][]int) []int { return at[r.IntN(len(at))] }
		var edits []string
		if at := name.FindAllStringIndex(statement, -1); len(at) > 0 {
			m := pick(at)
			edits = append(edits, statement[:m[1]]+"Q"+statement[m[1]:])
		}
		if at := number.FindAllStringIndex(statement, -1); len(at) > 0 {
			m := pick(at)
			n, _ := strconv.Atoi(statement[m[0]:m[1]])
			edits = append(edits, statement[:m[0]]+strconv.Itoa(n+1)+statement[m[1]:])
		}
		if at := operator.FindAllStringSubmatchIndex(statement, -1); len(at) > 0 {
			m := pick(at)
			edits = append(edits, statement[:m[2]]+swap[statement[m[2]:m[3]]]+statement[m[3]:])
		}
		if at := literal.FindAllStringIndex(statement, -1); len(at) > 0 {
			m := pick(at)
			edits = append(edits, statement[:m[0]+1]+" "+statement[m[0]+1:])
		}
		for _, e := range edits {
			if lawledger.Same(statement, e) {
				t.Errorf("%s: Same holds a changed law the same:\n\t%s\n\t%s", key, statement, e)
			}
		}
	}
}

// sortedKeys is m's keys in order, so the seeded picks replay.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
