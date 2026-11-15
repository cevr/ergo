package eval

import (
	"strconv"
	"strings"
)

// caseForm is how an obligation's cases are made, which decides how the
// native runner writes its verdict line.
type caseForm uint8

const (
	formNone   caseForm = iota // not run by a case plan (a machine law)
	formSingle                 // a test with no parameters: one run
	formListed                 // every value of a finite domain
	formDrawn                  // generated cases from the seed
)

// VerdictLine is r's verdict as the native test runner prints it, byte for
// byte (TESTSPEED FT1): "ok Name", "ok Name (100 cases)", "ok law L
// (proved: all 4 cases)", or a FAIL line with its case, its arguments
// (shrunk, and the drawn ones after "shrunk from:"), its report and, for a
// law, the line that replays it. source is the path ergo test names the
// program by, as the native runner's replay commands name it ("" when it
// names none: a test's replay is then left out, and a law's names file,
// the law's file).
//
// ok is false when the evaluator cannot write the native line: r is
// skipped, incomplete or not run, its report is one the evaluator does not
// write (Message is empty), or it is a machine law. That obligation's
// verdict is the native runner's (tier 2).
func VerdictLine(r Result, source string) (line string, ok bool) {
	if r.Machine != nil || r.form == formNone {
		return "", false
	}
	switch r.Status {
	case Passed:
		switch r.Evidence {
		case "evaluated":
			return "ok " + r.Name, true
		case "proved":
			return "ok " + r.Name + " (" + provedText(r.Cases) + ")", true
		case "tested":
			return "ok " + r.Name + " (" + strconv.Itoa(r.Cases) + " cases)", true
		}
		return "", false
	case Refuted, Defect:
	default:
		return "", false
	}
	if r.Message == "" {
		return "", false
	}
	law := strings.HasPrefix(r.Name, "law ")
	var b strings.Builder
	b.WriteString("FAIL " + r.Name)
	switch r.form {
	case formSingle:
	case formListed:
		b.WriteString(" (case " + strconv.Itoa(r.Case) + " of " + strconv.Itoa(r.TotalCases) + ", every value")
		writeArguments(&b, r.Arguments)
		b.WriteString(")")
	case formDrawn:
		b.WriteString(" (case " + strconv.Itoa(r.Case) + " of " + strconv.Itoa(r.TotalCases) + ", seed " + strconv.FormatUint(r.Seed, 10) + ", size " + strconv.Itoa(r.Case/4))
		writeArguments(&b, r.Arguments)
		if r.Drawn != nil && r.Status == Refuted {
			b.WriteString("; shrunk from")
			writeArguments(&b, r.Drawn)
		}
		if source != "" && !law && r.Status == Refuted {
			b.WriteString("; replay: ergo test " + source + " --seed " + strconv.FormatUint(r.Seed, 10))
		}
		b.WriteString(")")
	}
	b.WriteString(": " + r.Message)
	if law && r.Status == Refuted {
		file := source
		if file == "" {
			file = nativeSite(r.Site)
			file = file[:strings.Index(file, ":")]
		}
		b.WriteString("\n\treplay: ergo test " + file + " --law " + r.title)
		if r.form == formDrawn {
			b.WriteString(" --seed " + strconv.FormatUint(r.Seed, 10))
		}
	}
	return b.String(), true
}

// writeArguments writes ": a = 1, b = 2", nothing for no arguments.
func writeArguments(b *strings.Builder, args []Argument) {
	for i, a := range args {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString(", ")
		}
		b.WriteString(a.Name + " = " + a.Value)
	}
}

// provedText is a listed run's evidence as the native runner writes it.
func provedText(cases int) string {
	if cases == 1 {
		return "proved: 1 case"
	}
	return "proved: all " + strconv.Itoa(cases) + " cases"
}
