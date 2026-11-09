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

// CSVPosition is csv.Position: where a field starts in the text, as Go's
// Reader.FieldPos gives it. Line and Column count from 1, and Column
// counts bytes; a quoted field starts at its opening quote.
type CSVPosition struct {
	Line   int
	Column int
}

// CSVScan reads text in one pass, as a loop over Go's Reader.Read does,
// and calls each with every record and where each of its fields starts
// (Reader.FieldPos), in order, until each returns false or the text ends.
// It returns the error that stopped Go's reader, or a CSVParseError with
// no Reason when the text ended or each stopped the read: a stop reads no
// further record (fs3). Rows is a Seq over it. It takes a function, so it
// is not marked pure (stdlib.md, Pure bodies 2).
func CSVScan(text string, each func(fields []string, at []CSVPosition) bool) CSVParseError {
	return csvScan(strings.NewReader(text), each)
}

// csvScan is CSVScan over r, which a test reads the rest of to see that a
// stop ends the read.
func csvScan(r *strings.Reader, each func(fields []string, at []CSVPosition) bool) CSVParseError {
	rd := csv.NewReader(r)
	for {
		rec, err := rd.Read()
		if err != nil {
			if pe, ok := err.(*csv.ParseError); ok {
				return CSVParseError{Line: pe.Line, Column: pe.Column, Reason: pe.Err.Error(), Message: pe.Error()}
			}
			// io.EOF by its text: this file imports no io, so that its
			// marked bodies stay pure (Pure bodies 2).
			if err.Error() == "EOF" {
				return CSVParseError{}
			}
			return CSVParseError{Reason: err.Error(), Message: err.Error()}
		}
		at := make([]CSVPosition, len(rec))
		for i := range rec {
			at[i].Line, at[i].Column = rd.FieldPos(i)
		}
		if !each(rec, at) {
			return CSVParseError{}
		}
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
