package lawcache

import (
	"strconv"
	"strings"
)

// Verdict is a law run's verdict that held: its evidence, proved or
// tested, the cases it took, and the text of its ok line, the part in
// parentheses after the run's label ("100 cases", "proved: all 12
// cases"). Check-time evaluation and the test build write it and read it
// alike, so either runner's verdict serves the other.
type Verdict struct {
	Evidence string
	Cases    int
	Text     string
}

// Encode is the verdict as the store keeps it: evidence, cases and text,
// tab-separated.
func (v Verdict) Encode() string {
	return v.Evidence + "\t" + strconv.Itoa(v.Cases) + "\t" + v.Text
}

// Decode reads an encoded verdict.
func Decode(s string) (Verdict, bool) {
	parts := strings.SplitN(s, "\t", 3)
	if len(parts) != 3 || (parts[0] != "proved" && parts[0] != "tested") || strings.Contains(parts[2], "\n") {
		return Verdict{}, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return Verdict{}, false
	}
	return Verdict{Evidence: parts[0], Cases: n, Text: parts[2]}, true
}

// Evaluated is the verdict of a run that held over cases, its evidence
// proved (a listed run that took every value) or tested, with the text
// the test build prints for it.
func Evaluated(evidence string, cases int) Verdict {
	if evidence == "proved" {
		return Verdict{Evidence: "proved", Cases: cases, Text: "proved: all " + strconv.Itoa(cases) + " cases"}
	}
	return Verdict{Evidence: "tested", Cases: cases, Text: strconv.Itoa(cases) + " cases"}
}

// FromLine reads the verdict of the run label from a line the test build
// printed: `ok LABEL (TEXT)`. Any other line, a cached one included,
// gives none.
func FromLine(label, line string) (Verdict, bool) {
	rest, ok := strings.CutPrefix(line, "ok "+label+" (")
	if !ok || !strings.HasSuffix(rest, ")") || strings.HasSuffix(rest, ") (cached)") {
		return Verdict{}, false
	}
	text := strings.TrimSuffix(rest, ")")
	v := Verdict{Evidence: "tested", Text: text}
	if strings.HasPrefix(text, "proved") {
		v.Evidence = "proved"
	}
	v.Cases = leadingCount(strings.TrimPrefix(strings.TrimPrefix(text, "proved: "), "all "))
	return v, true
}

// leadingCount is the decimal number s begins with, 0 for none.
func leadingCount(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// Line is the line a runner prints for a cached verdict of the run label.
func (v Verdict) Line(label string) string {
	return "ok " + label + " (" + v.Text + ") (cached)"
}
