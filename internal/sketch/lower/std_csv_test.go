package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdCSVLawsProbe restates the laws of std csv's laws.ergo as Ergo
// property tests, with the package's private helpers (spice, lf, sample,
// same) written out again.
const stdCSVLawsProbe = `import csv
import slices
import strings

func spice(s string) string {
	let crlf = strings.ReplaceAll(s, "Z", "\r\n")
	let cr = strings.ReplaceAll(crlf, "A", "\r")
	let lf = strings.ReplaceAll(cr, "z", "\n")
	let quote = strings.ReplaceAll(lf, "b", "\"")
	strings.ReplaceAll(quote, "a", ",")
}

func lf(s string) string {
	let mut out = s
	for strings.Contains(out, "\r\n") {
		out = strings.ReplaceAll(out, "\r\n", "\n")
	}
	out
}

func sample(rows [][]string) [][]string {
	let mut out = [][]string{}
	let mut width = 0
	for fields in rows {
		if len(fields) == 0 {
			continue
		}
		if width == 0 {
			width = len(fields)
		}
		let mut row = []string{}
		for i in 0..width {
			let f = if i < len(fields) { fields[i] } else { "" }
			row = row.Append(lf(spice(f)))
		}
		out = out.Append(row)
	}
	out
}

func same(a [][]string, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i in 0..len(a) {
		if !slices.Equal(a[i], b[i]) {
			return false
		}
	}
	true
}

test CsvRoundTrip(rows [][]string) {
	let kept = sample(rows)
	let back = csv.ReadAll(csv.Format(kept)) catch {
	case csv.ParseError:
		assert false
		kept
	}
	assert same(back, kept)
}

test CsvLinesCount(rows [][]string) {
	let kept = sample(rows)
	let mut lines = len(kept)
	for fields in kept {
		for f in fields {
			lines = lines + strings.Count(f, "\n")
		}
	}
	assert strings.Count(csv.Format(kept), "\n") == lines
}

test CsvFormatIsStable(rows [][]string) {
	let text = csv.Format(sample(rows))
	let back = csv.ReadAll(text) catch {
	case csv.ParseError:
		assert false
		[][]string{}
	}
	assert csv.Format(back) == text
}

test CsvRowsStops(s string) {
	let rows = csv.Rows(spice(s))
	let mut n = 0
	rows(func(r csv.Row) bool {
		n = n + 1
		true
	})
	for k in 1..n+2 {
		let mut calls = 0
		rows(func(r csv.Row) bool {
			calls = calls + 1
			calls < k
		})
		assert calls == (if k < n { k } else { n })
	}
}

test CsvRowsKeepTheRecordsBefore(rows [][]string) {
	let kept = sample(rows)
	let mut parsed = [][]string{}
	let mut ends = 0
	for r in csv.Rows(csv.Format(kept) + "x\"y\n") {
		match r {
		case csv.Parsed as p:
			parsed = parsed.Append(p.Fields)
		case csv.Malformed:
			ends = ends + 1
		}
	}
	assert ends == 1 && same(parsed, kept)
}

test CsvRowsFollowReadAll(text string) {
	let t = spice(text)
	let mut parsed = [][]string{}
	let mut ends = 0
	let mut last = csv.ParseError{Line: 0, Column: 0, Reason: "", Message: ""}
	for r in csv.Rows(t) {
		match r {
		case csv.Parsed as p:
			assert ends == 0
			parsed = parsed.Append(p.Fields)
		case csv.Malformed as m:
			ends = ends + 1
			last = m.Error
		}
	}
	let mut failed = false
	let all = csv.ReadAll(t) catch {
	case csv.ParseError as e:
		assert ends == 1 && last == e
		failed = true
		parsed
	}
	assert failed || ends == 0 && same(parsed, all)
}
`

// stdCSVLawStatements is the statement of each law the probe restates, by
// ledger key; lawledger.Run holds it equal to the ledger.
var stdCSVLawStatements = map[string]string{
	"csv FormatIsStable":           `FormatIsStable(rows [][]string) { let text = Format(sample(rows)) let back = ReadAll(text) catch { case ParseError: return false } Format(back) == text }`,
	"csv LinesCount":               `LinesCount(rows [][]string) { let kept = sample(rows) let mut lines = len(kept) for fields in kept { for f in fields { lines = lines + strings.Count(f, "\n") } } strings.Count(Format(kept), "\n") == lines }`,
	"csv RoundTrip":                `RoundTrip(rows [][]string) { let kept = sample(rows) let back = ReadAll(Format(kept)) catch { case ParseError: return false } same(back, kept) }`,
	"csv RowsFollowReadAll":        `RowsFollowReadAll(text string) { let t = spice(text) let mut parsed = [][]string{} let mut ends = 0 let mut last = ParseError{Line: 0, Column: 0, Reason: "", Message: ""} for r in Rows(t) { match r { case Parsed as p: if ends > 0 { return false } parsed = parsed.Append(p.Fields) case Malformed as m: ends = ends + 1 last = m.Error } } let all = ReadAll(t) catch { case ParseError as e: return ends == 1 && last == e } ends == 0 && same(parsed, all) }`,
	"csv RowsStops":                `RowsStops(s string) { let rows = Rows(spice(s)) let mut n = 0 rows(func(r Row) bool { n = n + 1 true }) for k in 1..n+2 { let mut calls = 0 rows(func(r Row) bool { calls = calls + 1 calls < k }) if calls != (if k < n { k } else { n }) { return false } } true }`,
	"csv RowsKeepTheRecordsBefore": `RowsKeepTheRecordsBefore(rows [][]string) { let kept = sample(rows) let mut parsed = [][]string{} let mut ends = 0 for r in Rows(Format(kept) + "x\"y\n") { match r { case Parsed as p: parsed = parsed.Append(p.Fields) case Malformed: ends = ends + 1 } } ends == 1 && same(parsed, kept) }`,
}

// TestStdCSVLawsRun runs the csv probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdCSVLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdcsv", stdCSVLawsProbe, stdCSVLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
