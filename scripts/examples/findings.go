package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The expected findings (findingsFile) are the checker's errors an example
// is red by record for until the unit it names lands: an unfed law whose
// provider needs a context no unit has built yet (types.md, Laws 10). The
// checker reports them as errors; the runner does not treat the affected
// package as tested. It reads each line as one finding of one package of
// one example, and:
//   - a diagnostic that matches a line is not a layout error; the runner
//     prints it as red by record, naming the owed unit;
//   - a line that no diagnostic matches is a layout error: the finding is
//     gone, so its line goes too, and the file empties itself as the owed
//     units land.
//
// It is the one such file (owner, wf10 q12), not expected-skips returning:
// a skip hides a law, a finding here is an error on record.

// findingsFile is the expected findings, relative to the repository root.
const findingsFile = "scripts/examples/expected-findings"

// finding is one line: `EXAMPLE PACKAGE CODE LAW PROVIDER UNIT`. EXAMPLE
// is the folder under examples/ or modules/ (modules/NAME), PACKAGE the
// package folder relative to it, LAW the law as the diagnostic names it,
// PROVIDER the type it cannot run at, UNIT the owed unit.
type finding struct {
	example, pkg, code, law, provider, unit string
	line                                    int
	fired                                   bool
}

func (f *finding) String() string {
	return fmt.Sprintf("%s:%d: %s %s %s law %s at %s (owed %s)", findingsFile, f.line, f.example, f.pkg, f.code, f.law, f.provider, f.unit)
}

// matches reports whether the diagnostic code/message of package label of
// example is f.
func (f *finding) matches(example, label, code, message string) bool {
	if f.example != example || f.pkg != label || f.code != code {
		return false
	}
	rest, ok := strings.CutPrefix(message, "law "+f.law)
	return ok && (strings.HasPrefix(rest, ",") || strings.HasPrefix(rest, " ")) &&
		strings.Contains(rest, " cannot run at "+f.provider+":")
}

// findings are the expected findings of one load of the layout.
type findings struct {
	list []*finding
}

// readFindings reads root's findingsFile; no file is no finding.
func readFindings(root string) (*findings, []string) {
	data, err := os.ReadFile(filepath.Join(root, findingsFile))
	if os.IsNotExist(err) {
		return &findings{}, nil
	}
	if err != nil {
		return &findings{}, []string{fmt.Sprintf("%s: %v", findingsFile, err)}
	}
	fs := &findings{}
	var problems []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 6 {
			problems = append(problems, fmt.Sprintf("%s:%d: want EXAMPLE PACKAGE CODE LAW PROVIDER UNIT, got %q", findingsFile, n, text))
			continue
		}
		fs.list = append(fs.list, &finding{example: fields[0], pkg: fields[1], code: fields[2], law: fields[3], provider: fields[4], unit: fields[5], line: n})
	}
	return fs, problems
}

// lawCheck is the text between a package's label and the checker's code in
// the problem moduleLawProblems makes of a diagnostic (laws.go).
const lawCheck = ": the law check reads the checked package, and the checker reports "

// apply splits a program's layout errors into unmatched problems and
// red-by-record notes. It returns the matched findings and marks them fired.
func (fs *findings) apply(problems []string) (rest, notes []string, matched []*finding) {
	for _, problem := range problems {
		if f := fs.match(problem); f != nil {
			f.fired = true
			notes = append(notes, fmt.Sprintf("red by record (owed %s): %s", f.unit, problem))
			if !slices.Contains(matched, f) {
				matched = append(matched, f)
			}
			continue
		}
		rest = append(rest, problem)
	}
	return rest, notes, matched
}

// runRedByRecord leaves the executable cases dark while their program
// has a recorded checker error. Unblocked packages still run their laws.
func (r *runner) runRedByRecord(p *program) {
	var owed []string
	for _, f := range p.findings {
		if !slices.Contains(owed, f.unit) {
			owed = append(owed, f.unit)
		}
	}
	fmt.Fprintf(r.out, "red by record: %s: cases not run (%d cases) until %s land\n", p.name, len(p.cases), strings.Join(owed, ", "))
	if !r.cfg.record {
		r.testProgram(p)
	}
}

// stale is a layout error for each finding no problem matched: the
// checker no longer reports it.
func (fs *findings) stale() []string {
	var problems []string
	for _, f := range fs.list {
		if !f.fired {
			problems = append(problems, fmt.Sprintf("%s: the checker no longer reports it; remove the line (its unit %s has landed, or the finding moved)", f, f.unit))
		}
	}
	return problems
}

// match is the finding problem reports, or nil. problem reads
// `examples/EXAMPLE: PACKAGE<lawCheck>CODE at FILE:LINE:COL: MESSAGE`
// (modules/NAME for a module).
func (fs *findings) match(problem string) *finding {
	head, tail, ok := strings.Cut(problem, lawCheck)
	if !ok {
		return nil
	}
	where, label, ok := strings.Cut(head, ": ")
	if !ok {
		return nil
	}
	example := strings.TrimPrefix(where, "examples/")
	code, at, ok := strings.Cut(tail, " at ")
	if !ok {
		return nil
	}
	_, message, ok := strings.Cut(at, ": ")
	if !ok {
		return nil
	}
	for _, f := range fs.list {
		if f.matches(example, label, code, message) {
			return f
		}
	}
	return nil
}
