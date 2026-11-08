package v1

import (
	"encoding/csv"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// csvText is a seeded text of up to 40 pieces, each a letter, a space, a
// comma, a quote, a CR, a LF or a CR LF, so it holds quoted and bare
// fields, quotes in both, multi-line fields, ragged rows and every error
// of Go's reader.
func csvText(r *rand.Rand) string {
	pieces := []string{"a", "b", "é", " ", ",", ",", "\"", "\"", "\r", "\n", "\n", "\r\n"}
	var b strings.Builder
	for range r.IntN(41) {
		b.WriteString(pieces[r.IntN(len(pieces))])
	}
	return b.String()
}

// csvRows is seeded rows of up to 4 records of 0 to 3 fields, each field
// up to 6 pieces of which some the writer quotes, so some rows are a lone
// empty field.
func csvRows(r *rand.Rand) [][]string {
	rows := make([][]string, r.IntN(5))
	for i := range rows {
		rows[i] = make([]string, r.IntN(4))
		for j := range rows[i] {
			var b strings.Builder
			for range r.IntN(7) {
				b.WriteString([]string{"a", " ", ",", "\"", "\r", "\n", "\r\n", `\.`}[r.IntN(8)])
			}
			rows[i][j] = b.String()
		}
	}
	return rows
}

// BenchmarkCSV: CSVReadAll and CSVFormat against Go's reader and writer
// on the same 2,000 rows of quoted and bare fields (about 100 KB).
func BenchmarkCSV(b *testing.B) {
	rows := make([][]string, 2000)
	for i := range rows {
		rows[i] = []string{"2026-11-08", "north, east", "widget \"pro\"", "12.50", "line one\nline two"}
	}
	text := CSVFormat(rows)
	b.Run("ReadAll/ergo", func(b *testing.B) {
		for range b.N {
			textSink, _ = CSVReadAll(text)
		}
	})
	b.Run("ReadAll/go", func(b *testing.B) {
		for range b.N {
			textSink, _ = csv.NewReader(strings.NewReader(text)).ReadAll()
		}
	})
	b.Run("Format/ergo", func(b *testing.B) {
		for range b.N {
			textSink = CSVFormat(rows)
		}
	})
	b.Run("Format/go", func(b *testing.B) {
		for range b.N {
			var s strings.Builder
			w := csv.NewWriter(&s)
			_ = w.WriteAll(rows)
			textSink = s.String()
		}
	})
}

// TestStdCSVMatchesGo (U4 std-csv; stdlib.md, csv): on 20,000 seeded
// texts, CSVReadAll gives Go's ReadAll rows or Go's *csv.ParseError as
// CSVParseError (Line, Column, Reason, Message), and CSVPrefix the records
// Go's Read gives before its first error; on 20,000 seeded rows,
// CSVFormat writes what Go's csv.Writer writes, except a lone empty field,
// which is "". It catches what the laws do not: a reader with other
// options (LazyQuotes, FieldsPerRecord), an error placed by its record's
// start line or a shifted column, a prefix that keeps the record of the
// failing Read, and a writer that ends rows with CR LF.
func TestStdCSVMatchesGo(t *testing.T) {
	r := rand.New(rand.NewPCG(4, 18))
	failed := 0
	for range 20000 {
		text := csvText(r)
		got, err := CSVReadAll(text)
		want, gerr := csv.NewReader(strings.NewReader(text)).ReadAll()
		if gerr != nil {
			pe := gerr.(*csv.ParseError)
			wantErr := CSVParseError{Line: pe.Line, Column: pe.Column, Reason: pe.Err.Error(), Message: pe.Error()}
			if err != wantErr {
				t.Fatalf("CSVReadAll(%q) fails %#v; Go %#v", text, err, wantErr)
			}
			failed++
		} else if err != nil || !slices.EqualFunc(got, want, slices.Equal) {
			t.Fatalf("CSVReadAll(%q) = %q, %v; Go %q", text, got, err, want)
		}
		rd := csv.NewReader(strings.NewReader(text))
		prefix := [][]string{}
		for {
			rec, err := rd.Read()
			if err != nil {
				break
			}
			prefix = append(prefix, rec)
		}
		if p := CSVPrefix(text); !slices.EqualFunc(p, prefix, slices.Equal) {
			t.Fatalf("CSVPrefix(%q) = %q; Go %q", text, p, prefix)
		}
	}
	if failed < 2000 || failed > 18000 {
		t.Fatalf("%d of 20000 texts fail to read; the generator should give both outcomes often", failed)
	}
	for range 20000 {
		rows := csvRows(r)
		var b strings.Builder
		w := csv.NewWriter(&b)
		for _, rec := range rows {
			if len(rec) == 1 && rec[0] == "" {
				w.Flush()
				b.WriteString("\"\"\n")
				continue
			}
			if err := w.Write(rec); err != nil {
				t.Fatal(err)
			}
		}
		w.Flush()
		if got := CSVFormat(rows); got != b.String() {
			t.Fatalf("CSVFormat(%q) = %q; Go %q", rows, got, b.String())
		}
	}
}
