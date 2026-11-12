package lint

import "strings"

// Text helpers of the fixes. They copy and re-indent source text the
// rules located through checked facts; none of them decides what a rule
// matches.

// lineIndent is the leading tabs and spaces of the line that holds the
// byte at.
func lineIndent(src string, at int) string {
	start := strings.LastIndexByte(src[:at], '\n') + 1
	end := start
	for end < len(src) && (src[end] == '\t' || src[end] == ' ') {
		end++
	}
	return src[start:end]
}

// outdent removes extra from the start of each line of text after the
// first, so text moved out of extra levels of nesting keeps the
// formatter's layout. It reports false when a line does not start with
// extra (text it cannot move without reformatting).
func outdent(text, extra string) (string, bool) {
	if extra == "" {
		return text, true
	}
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		rest, ok := strings.CutPrefix(lines[i], extra)
		if !ok {
			return "", false
		}
		lines[i] = rest
	}
	return strings.Join(lines, "\n"), true
}

// extraIndent is what inner adds to outer, when inner starts with outer.
func extraIndent(outer, inner string) (string, bool) {
	return strings.CutPrefix(inner, outer)
}

// timeoutPackage is the std package race-for-timeout's fix imports, and
// the name it binds when the file does not import it yet.
const timeoutPackage = "timeout"

// timeoutFix is the text that replaces a race of body against a sleep of
// d that then gives fallback: body under timeout.After, the fallback in a
// catch arm of timeout.Expired, which a reasoned silent-catch suppression
// explains. qual is the name the file binds std timeout to; indent is the
// race's line indent.
func timeoutFix(qual, d, body, fallback, indent string) string {
	var b strings.Builder
	b.WriteString(qual + ".After(" + d + ", " + body + ") catch {\n")
	b.WriteString(indent + "// " + DirectiveWord + " silent-catch -- the deadline's fallback, as the race against a sleep gave it\n")
	b.WriteString(indent + "case " + qual + ".Expired:\n")
	b.WriteString(indent + "\t" + fallback + "\n")
	b.WriteString(indent + "}")
	return b.String()
}
