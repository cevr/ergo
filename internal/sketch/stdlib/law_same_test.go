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
// key, and the keys of the member rewrite laws among them. A derived or
// by-name pair law has none (lawStatement).
func stdLawSources(t *testing.T) (map[string]string, map[string]bool) {
	t.Helper()
	out, rewrites := map[string]string{}, map[string]bool{}
	add := func(key string, f *syntax.File, d *syntax.LawDecl) {
		if d.Derived == nil && d.Pair == nil {
			out[key] = f.Src[d.Name.Pos():d.Body.End()]
			if d.Rewrite != nil {
				rewrites[key] = true
			}
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
	return out, rewrites
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
// every std law with source text: Same holds the source's statement the
// same as the formatter's text of the law; and, respaced as above where
// the formatter prints the respaced law as it prints the source (it keeps
// a list's layout, which the respacing may change), the same as the
// respaced law; at least half the laws are compared so. A member rewrite
// law, which the formatter cannot take alone, is skipped. Over seeded single-token edits of each statement (a name, an
// operator, a number, a space inside a string) Same holds them different.
// A failing law prints its key and the edit; the seed is fixed (1).
//
// Mutants only it kills (internal/lawledger/testdata/mutants.json):
// same-paren-in-word, same-interpolation-as-text,
// same-licence-skips-plain-law.
func TestLawledgerSameIsTheFormatters(t *testing.T) {
	sources, rewrites := stdLawSources(t)
	if len(sources) == 0 {
		t.Fatal("no std law with source text")
	}
	r := rand.New(rand.NewPCG(1, 0))
	name := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	number := regexp.MustCompile(`\b[0-9]+\b`)
	operator := regexp.MustCompile(` (==|!=|&&|\|\||\+|<|>) `)
	swap := map[string]string{"==": "!=", "!=": "==", "&&": "||", "||": "&&", "+": "-", "<": ">", ">": "<"}
	alone, layout, respacedSame := 0, 0, 0
	for _, key := range sortedKeys(sources) {
		src := sources[key]
		statement := lawledger.Normalize(src)
		respaced := outsideLiterals(src, unformat.Replace)
		format := func(text string) (string, error) {
			out, err := hostsyntax.Source("law " + text + "\n")
			if err != nil {
				if wrapped, werr := hostsyntax.Source("type _ {\n\tlaw " + text + ",\n}\n"); werr == nil {
					if inner, ok := strings.CutPrefix(wrapped, "type _ {\n\tlaw "); ok {
						inner = strings.TrimSuffix(strings.TrimSpace(inner), "}")
						inner = strings.TrimSuffix(strings.TrimSpace(inner), ",")
						return lawledger.Normalize(inner), nil
					}
				}
			}
			return lawledger.Normalize(strings.TrimPrefix(out, "law ")), err
		}
		fromSource, err := format(src)
		if err != nil {
			// A member rewrite law ({ a; b } == { c }) is no top-level
			// declaration, so the formatter cannot take it alone (a
			// formatter gap: it formats the law only in its type). No
			// other law may be skipped.
			if !rewrites[key] {
				t.Errorf("%s: the formatter refuses the law, which is no member rewrite law: %v", key, err)
			}
			alone++
			continue
		}
		// The formatter's text of the law is the law; so it is to Same.
		if !lawledger.Same(statement, fromSource) {
			t.Errorf("%s: Same refuses the formatter's text of the law:\n\t%s\n\t%s", key, statement, fromSource)
		}
		fromRespaced, err := format(respaced)
		switch {
		case err != nil:
			t.Errorf("%s: the formatter refuses the respaced law %q: %v", key, respaced, err)
		case fromRespaced != fromSource:
			// The formatter keeps a list's layout (one element a line), which
			// the respacing can change: no claim about this law.
			layout++
		case !lawledger.Same(statement, respaced):
			t.Errorf("%s: Same refuses a respacing the formatter undoes:\n\t%s\n\t%s", key, statement, lawledger.Normalize(respaced))
		default:
			respacedSame++
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
		// A space inside a string where it has no white space beside it:
		// a run's length is the ledger's to drop (lawStatement collapses
		// each run, inside literals too), a new space is not.
		// An interpolation's code is tokens, so a space in it is no edit.
		var gaps []int
		for _, m := range literal.FindAllStringIndex(statement, -1) {
			depth := 0
			for p := m[0] + 1; p < m[1]-1; p++ {
				switch {
				case depth == 0 && statement[p-1] == '\\' && statement[p] == '(':
					depth = 1
				case depth > 0 && statement[p] == '(':
					depth++
				case depth > 0 && statement[p] == ')':
					depth--
				case depth == 0 && statement[p-1] != ' ' && statement[p] != ' ':
					gaps = append(gaps, p)
				}
			}
		}
		if len(gaps) > 0 {
			p := gaps[r.IntN(len(gaps))]
			edits = append(edits, statement[:p]+" "+statement[p:])
		}
		for _, e := range edits {
			if lawledger.Same(statement, e) {
				t.Errorf("%s: Same holds a changed law the same:\n\t%s\n\t%s", key, statement, e)
			}
		}
	}
	t.Logf("%d laws: %d respaced and held the same, %d kept in the formatter's layout, %d skipped as member rewrite laws the formatter cannot take alone", len(sources), respacedSame, layout, alone)
	if alone > len(rewrites) {
		t.Errorf("%d laws skipped, but std has %d member rewrite laws", alone, len(rewrites))
	}
	if respacedSame*2 < len(sources) {
		t.Errorf("only %d of %d laws were respaced and compared", respacedSame, len(sources))
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
