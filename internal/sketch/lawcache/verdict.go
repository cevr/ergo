package lawcache

import (
	"strconv"
	"strings"
)

// Verdict is a law run's verdict that held: its evidence, proved or
// tested, the cases it took, and the text of its ok line, the part in
// parentheses after the run's label ("100 cases", "proved: all 12
// cases"), then, after a line break, the lines the run printed after it
// (a search's witness). Check-time evaluation and the test build write
// it and read it alike, so either runner's verdict serves the other.
type Verdict struct {
	Evidence string
	Cases    int
	Text     string
	// Reads are the paths of the packages whose bodies the run read,
	// sorted, when ReadsKnown: a verdict is valid for what it read, so a
	// cache may keep it across an edit to any other package (GB-WS0C). A
	// test binary's verdict does not know them.
	Reads      []string
	ReadsKnown bool
}

// verdictFormat is the encoding's version: a verdict of an older one is
// not read (it would say nothing of its reads), so its run runs once.
const verdictFormat = "v2"

// Encode is the verdict as the store keeps it, tab-separated: the format,
// evidence, cases, the text as a Go quoted string, and the reads as one
// quoted string of lines, or "?" when not known.
func (v Verdict) Encode() string {
	reads := "?"
	if v.ReadsKnown {
		reads = strconv.Quote(strings.Join(v.Reads, "\n"))
	}
	return verdictFormat + "\t" + v.Evidence + "\t" + strconv.Itoa(v.Cases) + "\t" + strconv.Quote(v.Text) + "\t" + reads
}

// Decode reads an encoded verdict.
func Decode(s string) (Verdict, bool) {
	parts := strings.Split(s, "\t")
	if len(parts) != 5 || parts[0] != verdictFormat || (parts[1] != "proved" && parts[1] != "tested") {
		return Verdict{}, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 0 {
		return Verdict{}, false
	}
	text, err := strconv.Unquote(parts[3])
	if err != nil {
		return Verdict{}, false
	}
	v := Verdict{Evidence: parts[1], Cases: n, Text: text}
	if parts[4] != "?" {
		reads, err := strconv.Unquote(parts[4])
		if err != nil {
			return Verdict{}, false
		}
		v.ReadsKnown = true
		if reads != "" {
			v.Reads = strings.Split(reads, "\n")
		}
	}
	return v, true
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

// FromLines reads the verdict of the run label from the test build's
// output lines at its ok line, lines[0], with the lines that continue it
// (a blank line or one that begins with a tab, as a search's witness is
// printed), and reports how many lines it read.
func FromLines(label string, lines []string) (Verdict, int, bool) {
	v, ok := FromLine(label, lines[0])
	if !ok {
		return Verdict{}, 1, false
	}
	n := 1
	for n < len(lines) && (lines[n] == "" || strings.HasPrefix(lines[n], "\t")) {
		n++
	}
	end := n
	for end > 1 && lines[end-1] == "" {
		end--
	}
	if end > 1 {
		v.Text += "\n" + strings.Join(lines[1:end], "\n")
	}
	return v, n, true
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

// Line is the text a runner prints for a cached verdict of the run label:
// its ok line, marked, and the lines that followed it.
func (v Verdict) Line(label string) string {
	head, rest, _ := strings.Cut(v.Text, "\n")
	if rest != "" {
		rest = "\n" + rest
	}
	return "ok " + label + " (" + head + ") (cached)" + rest
}
