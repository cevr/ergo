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
// skipped, incomplete, not run or a defect (the native run stops at a
// defect, and the runtime reports its reasons), its report is one the
// evaluator does not write (Message is empty), or it is a machine law. That obligation's
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
			text := strconv.Itoa(r.Cases) + " cases"
			if r.ExtraStaleCases > 0 {
				text += ", " + strconv.Itoa(r.ExtraStaleCases) + " extra case(s): digest changed"
			}
			return "ok " + r.Name + " (" + text + ")", true
		}
		return "", false
	case Refuted:
	default:
		// A defect stops the native run: the obligations after it print
		// nothing, and the runtime reports its reasons. Tier 2 answers it.
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
		if r.StoredCaseHit {
			if r.StoredStale {
				b.WriteString(" (case 0, extra case, digest changed")
			} else {
				b.WriteString(" (case 0, stored case")
			}
			writeArguments(&b, r.Arguments)
			if r.Drawn != nil {
				b.WriteString("; shrunk from")
				writeArguments(&b, r.Drawn)
			}
			b.WriteString(")")
			break
		}
		b.WriteString(" (case " + strconv.Itoa(r.Case) + " of " + strconv.Itoa(r.TotalCases) + ", seed " + strconv.FormatUint(r.Seed, 10) + ", size " + strconv.Itoa(r.Case/4))
		writeArguments(&b, r.Arguments)
		if r.Drawn != nil {
			b.WriteString("; shrunk from")
			writeArguments(&b, r.Drawn)
		}
		if source != "" && !law {
			b.WriteString("; replay: ergo test " + source + " --seed " + strconv.FormatUint(r.Seed, 10))
		}
		b.WriteString(")")
	}
	b.WriteString(": " + r.Message)
	if law {
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

// Summary is the native test run of a package whose results are rep's
// Results, in the order the native runner prints them (Report.Results is:
// the tests in source order, then each law run, a law's bindings and
// contexts in order): its standard output, line by line (each verdict
// line, then "PASS (n tests)" or "FAIL (k of n tests)"), the line its
// runtime writes to standard error ("error: k of n tests failed", "" on a
// pass) and its exit status (1 when a test failed, else 0), byte for byte
// (TESTSPEED FT1).
//
// ok is false unless the evaluator decides every result (VerdictLine): a
// package with a skipped, incomplete, defective or machine-law obligation,
// or with none at all, is the native runner's whole. Filters (--run,
// --law, --skip-law) are the native runner's too.
func Summary(results []Result, source string) (stdout []string, stderr string, exit int, ok bool) {
	if len(results) == 0 {
		return nil, "", 0, false
	}
	failed := 0
	for _, r := range results {
		line, decided := VerdictLine(r, source)
		if !decided {
			return nil, "", 0, false
		}
		stdout = append(stdout, line)
		if r.Status != Passed {
			failed++
		}
	}
	n := strconv.Itoa(len(results))
	if failed > 0 {
		k := strconv.Itoa(failed)
		return append(stdout, "FAIL ("+k+" of "+n+" tests)"), "error: " + k + " of " + n + " tests failed", 1, true
	}
	return append(stdout, "PASS ("+n+" tests)"), "", 0, true
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
