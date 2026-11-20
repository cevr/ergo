package fix

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// fragmentFile reports how the string literals of a Go file that do not
// parse whole are read (MutFragment): a generator gen_*.go (irspike's,
// which its tests run) joins pieces of Ergo source, `"func bump(s *S, k
// int) int {\n"` with its body written by the next call, so its pieces
// are read with closing braces tried after them (closes); a test's
// literals are read whole, with only the fmt verbs of a format stood in
// for, since a test's pieces are mostly Go that it expects of the
// lowering (`func evens(env *Env, n int) []int {`), which parses as Ergo
// too once closed; a test's piece that writes a word only Ergo writes
// (ergoMarker) is closed as a generator's is. Any other file's code emits Go, whose `*T` is Go's
// pointer.
func fragmentFile(file string) (read, closes bool) {
	base := filepath.Base(file)
	switch {
	case strings.HasPrefix(base, "gen_"):
		return true, true
	case strings.HasSuffix(base, "_test.go"):
		return true, false
	}
	return false, false
}

// ergoMarkerRE matches a word only Ergo writes at the start of a statement
// or a declaration: an unquoted import (Go quotes its paths), let, match,
// or a fails or needs clause.
var ergoMarkerRE = regexp.MustCompile(`(^|\n)import [a-z]|\blet |\bmatch |\bfails |\bneeds `)

// ergoMarker reports a test's literal that is Ergo, so that its open piece
// (a program a generator ends after it) is closed and read like a
// generator's (fragmentFile).
func ergoMarker(text string) bool { return ergoMarkerRE.MatchString(text) }

// verbRE matches a fmt verb: `%d`, `%5.2f`, `%%`.
var verbRE = regexp.MustCompile(`%[-+# 0-9.*]*[a-zA-Z%]`)

// fragmentCloses are the texts tried after a fragment, so that a function
// whose body a later literal writes parses: none, then one to three
// closing braces.
var fragmentCloses = []string{"", "\n}\n", "\n}\n}\n", "\n}\n}\n}\n"}

// MutFragment respells the borrows of text, a piece of an Ergo source
// that does not parse whole (MODES-1 for generators; RESPELL-SYNTAX). Each
// fmt verb stands as digits of its own length (`%d` as `00`, `%%` as
// `% `), so every offset is the text's, and closing braces are tried
// after it; the first form that parses gives the borrows, and each one
// inside text is rewritten there; without closes only text itself is
// tried. A form whose borrows name a Go package, or that writes a borrow
// as a result (goSource), is Go and is left. n is 0 when no form parses or
// none has a borrow.
func MutFragment(text string, closes bool) (out string, n int) {
	shaped := verbRE.ReplaceAllStringFunc(text, func(v string) string {
		if v == "%%" {
			return "% "
		}
		return strings.Repeat("0", len(v))
	})
	tries := fragmentCloses
	if !closes {
		tries = tries[:1]
	}
	for _, close := range tries {
		f, err := syntax.Parse(shaped + close)
		if err != nil {
			continue
		}
		if goSource(shaped + close) {
			return text, 0
		}
		var all []mutEdit
		for _, at := range borrowStars(f) {
			all = append(all, mutEdit{at: int(at), del: 1, ins: "mut "})
		}
		all = append(all, lentEdits(f)...) // [T lent] as Mut deletes it
		var eds []mutEdit
		for _, e := range all {
			if e.at+e.del <= len(text) {
				eds = append(eds, e)
			}
		}
		sort.Slice(eds, func(i, j int) bool { return eds[i].at < eds[j].at })
		if len(eds) == 0 {
			return text, 0
		}
		out = text
		for i := len(eds) - 1; i >= 0; i-- {
			e := eds[i]
			out = out[:e.at] + e.ins + out[e.at+e.del:]
		}
		return out, len(eds)
	}
	return text, 0
}
