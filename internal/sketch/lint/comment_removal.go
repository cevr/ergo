package lint

import "strings"

// removal is the edit that takes the comment [from, to) out of src. A
// comment alone on its lines goes with its lines, and with one blank line
// beside them where that would leave two in a row, a blank line after an
// opening or before a closing bracket, or one at the start or the end of
// the file. A comment after code goes with the spaces before it, one
// before code with the spaces after it. A block comment between code on
// one line goes with a space; one that spans lines between code becomes
// a line break, which it counted as for semicolons.
func removal(src string, from, to int) (int, int, string) {
	start := startOfLine(src, from)
	end := endOfLine(src, to)
	codeBefore := strings.TrimLeft(src[start:from], " \t") != ""
	codeAfter := strings.TrimRight(src[to:end], " \t\r") != ""
	switch {
	case codeBefore && codeAfter:
		if strings.Contains(src[from:to], "\n") {
			return from, to, "\n"
		}
		if to < len(src) && src[to] == ' ' {
			return from, to + 1, ""
		}
		if from > 0 && src[from-1] == ' ' {
			return from - 1, to, ""
		}
		return from, to, ""
	case codeBefore:
		for from > start && (src[from-1] == ' ' || src[from-1] == '\t') {
			from--
		}
		return from, end, ""
	case codeAfter:
		for to < end && (src[to] == ' ' || src[to] == '\t') {
			to++
		}
		return from, to, ""
	}
	if end < len(src) {
		end++ // the line break
	}
	prev, next := lineBefore(src, start), lineFrom(src, end)
	prevBlank := prev == nil || blank(src[prev[0]:prev[1]])
	nextBlank := next == nil || blank(src[next[0]:next[1]])
	switch {
	case prev == nil && next != nil && nextBlank:
		end = next[1] + lineBreak(src, next[1])
	case next == nil && prev != nil && prevBlank:
		start = prev[0]
	case prev != nil && next != nil && prevBlank && nextBlank:
		end = next[1] + lineBreak(src, next[1])
	case prev != nil && next != nil && nextBlank && opens(src[prev[0]:prev[1]]):
		end = next[1] + lineBreak(src, next[1])
	case prev != nil && next != nil && prevBlank && closes(src[next[0]:next[1]]):
		start = prev[0]
	}
	return start, end, ""
}

func startOfLine(src string, pos int) int {
	for pos > 0 && src[pos-1] != '\n' {
		pos--
	}
	return pos
}

func endOfLine(src string, pos int) int {
	for pos < len(src) && src[pos] != '\n' {
		pos++
	}
	return pos
}

func lineBreak(src string, end int) int {
	if end < len(src) {
		return 1
	}
	return 0
}

// lineBefore is the bounds of the line before the one that starts at
// start, without its line break; nil at the first line.
func lineBefore(src string, start int) []int {
	if start == 0 {
		return nil
	}
	return []int{startOfLine(src, start-1), start - 1}
}

// lineAt is the bounds of the line that starts at start; nil past the
// end of src.
func lineFrom(src string, start int) []int {
	if start >= len(src) {
		return nil
	}
	return []int{start, endOfLine(src, start)}
}

func blank(line string) bool { return strings.TrimSpace(line) == "" }

func opens(line string) bool {
	t := strings.TrimRight(line, " \t\r")
	return strings.HasSuffix(t, "{") || strings.HasSuffix(t, "(") || strings.HasSuffix(t, "[")
}

func closes(line string) bool {
	t := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(t, "}") || strings.HasPrefix(t, ")") || strings.HasPrefix(t, "]")
}
