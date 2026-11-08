package v1

import (
	"encoding/csv"
	"strings"
)

// The pure bodies of std csv (docs/spec/stdlib.md, csv; std-csv U4): Go's
// encoding/csv with its defaults (a comma, RFC 4180 quotes, LazyQuotes
// off, the field count of the first record held for every other), on a
// strings.Reader or a strings.Builder, so they read and write no host
// state. Each is marked //ergo:pure.

// CSVParseError is csv.ParseError, the failure of ReadAll: Go's
// *csv.ParseError as a record. Line and Column are where the error is
// (Column counts bytes from 1), Reason is the text of Go's Err
// ("wrong number of fields") and Message Go's whole text of the error.
type CSVParseError struct {
	Line    int
	Column  int
	Reason  string
	Message string
}

// Error is the failure's text, Go's.
func (e CSVParseError) Error() string { return e.Message }

// CSVReadAll is Go's csv.NewReader(strings.NewReader(text)).ReadAll():
// every record of text, or the first error as CSVParseError. A text with
// no record is no rows, not nil.
//
//ergo:pure
func CSVReadAll(text string) ([][]string, error) {
	rows, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		if pe, ok := err.(*csv.ParseError); ok {
			return nil, CSVParseError{Line: pe.Line, Column: pe.Column, Reason: pe.Err.Error(), Message: pe.Error()}
		}
		return nil, CSVParseError{Reason: err.Error(), Message: err.Error()}
	}
	if rows == nil {
		rows = [][]string{}
	}
	return rows, nil
}

// CSVPrefix is the records of text that Go's reader reads before its
// first error, or every record when there is none. Rows yields them
// before the error.
//
//ergo:pure
func CSVPrefix(text string) [][]string {
	r := csv.NewReader(strings.NewReader(text))
	rows := [][]string{}
	for {
		rec, err := r.Read()
		if err != nil {
			return rows
		}
		rows = append(rows, rec)
	}
}

// CSVFormat is rows as Go's csv.Writer writes them, each ended by a line
// feed, with one change: a row of one empty field is written "" where Go
// writes an empty line, which a reader skips, so that every row with a
// field reads back (law RoundTrip).
//
//ergo:pure
func CSVFormat(rows [][]string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	for _, rec := range rows {
		if len(rec) == 1 && rec[0] == "" {
			w.Flush()
			b.WriteString("\"\"\n")
			continue
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return b.String()
}
