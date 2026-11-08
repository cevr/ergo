package check_test

import "testing"

// TestForOverStringRunes (L4 ln-runes-loop; types.md, Loops 1): a for over
// a string, or a type defined over one, binds the byte offset as an int
// and the rune as a rune (int32); a byte or a string is not the element,
// and a for over a pointer to a string still names `*p`. Each case lists
// its diagnostics, CODE:line.
func TestForOverStringRunes(t *testing.T) {
	runD1(t, []d1Case{
		{"index-and-rune", "func f(s string) int {\n\tlet mut n = 0\n\tfor i, r in s {\n\t\tlet j int = i\n\t\tlet c rune = r\n\t\tn += j + int(c)\n\t}\n\tn\n}\n", nil},
		{"rune-only", "func f(s string) rune {\n\tlet mut m rune = 0\n\tfor r in s {\n\t\tm = r\n\t}\n\tm\n}\n", nil},
		{"index-only", "func f(s string) int {\n\tlet mut n = 0\n\tfor i, _ in s {\n\t\tn = i\n\t}\n\tn\n}\n", nil},
		{"defined-string", "type Name string\n\nfunc f(s Name) rune {\n\tlet mut m rune = 0\n\tfor r in s {\n\t\tm = r\n\t}\n\tm\n}\n", nil},
		{"literal", "func f() int {\n\tlet mut n = 0\n\tfor r in \"héllo\" {\n\t\tn += int(r)\n\t}\n\tn\n}\n", nil},
		{"rune-is-no-byte", "func f(s string) byte {\n\tlet mut m byte = 0\n\tfor r in s {\n\t\tm = r\n\t}\n\tm\n}\n", []string{"S108:4"}},
		{"rune-is-no-string", "func f(s string) string {\n\tlet mut m = \"\"\n\tfor r in s {\n\t\tm = r\n\t}\n\tm\n}\n", []string{"S108:4"}},
		{"index-is-no-int64", "func f(s string) int64 {\n\tlet mut m int64 = 0\n\tfor i, _ in s {\n\t\tm = i\n\t}\n\tm\n}\n", []string{"S108:4"}},
		{"unread-rune", "func f(s string) {\n\tfor r in s {\n\t}\n}\n", []string{"S110:2"}},
		{"pointer", "func f(p *string) {\n\tfor r in p {\n\t\tlet _ = r\n\t}\n}\n", []string{"S108:2"}},
		{"three-names", "func f(s string) {\n\tfor i, r, x in s {\n\t}\n}\n", []string{"S001:2"}},
	})
}
