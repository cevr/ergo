package fix

import (
	"path/filepath"
	"regexp"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// fragmentFile reports a Go file whose string literals may hold pieces of
// Ergo source that a generator joins: a generator gen_*.go (irspike's,
// which its tests run), `"func bump(s *S, k int) int {\n"` with its body
// written by the next call, or a fmt format with a verb in it.
// MutFragment reads its literals that do not parse whole. A test's pieces
// are mostly Go that a test expects of the lowering (`func evens(env *Env,
// n int) []int {`), whose `*T` is Go's pointer and parses as Ergo too, so
// a test's literal is respelled only when it parses whole (Mut).
func fragmentFile(file string) bool {
	return strings.HasPrefix(filepath.Base(file), "gen_")
}

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
// inside text is rewritten there. A form whose borrows name a Go package
// (goSource) is Go and is left. n is 0 when no form parses or none has a
// borrow.
func MutFragment(text string) (out string, n int) {
	shaped := verbRE.ReplaceAllStringFunc(text, func(v string) string {
		if v == "%%" {
			return "% "
		}
		return strings.Repeat("0", len(v))
	})
	for _, close := range fragmentCloses {
		f, err := syntax.Parse(shaped + close)
		if err != nil {
			continue
		}
		if goSource(shaped + close) {
			return text, 0
		}
		var eds []mutEdit
		for _, at := range borrowStars(f) {
			if int(at) < len(text) {
				eds = append(eds, mutEdit{at: int(at), del: 1, ins: "mut "})
			}
		}
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
