package report

import (
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// DiagnosticReportSchemaVersion versions the diagnostic report. Version 2
// dropped the lint fields and the suppression list (#205).
const DiagnosticReportSchemaVersion = 2

// SourceSnapshot identifies the exact text a result was computed from. The
// text stays out of JSON. A program read from a directory has no one text:
// Files maps each file a diagnostic can name (Diagnostic.File) to its text,
// and a diagnostic with a File is projected onto that text.
type SourceSnapshot struct {
	URI    string
	Origin string
	Text   string            `json:"-"`
	Files  map[string]string `json:"-"`
}

// FileURI is the file URI of the requested document, not of its symlink
// target. It never reads the file system, so a replacement during checking
// cannot attach the checked bytes to another document. Relative and
// absolute spellings of one path share an escaped URI.
func FileURI(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String(), nil
}

// SourceIdentity names the document a report describes.
type SourceIdentity struct {
	URI    string `json:"uri"`
	Origin string `json:"origin"`
}

// DiagnosticPosition is a zero-based LSP position: a line and a count of
// UTF-16 code units.
type DiagnosticPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// DiagnosticRange is an end-exclusive LSP range.
type DiagnosticRange struct {
	Start DiagnosticPosition `json:"start"`
	End   DiagnosticPosition `json:"end"`
}

// LSPDiagnostic is the plain-text LSP projection of a finding. Characters
// count UTF-16 code units, the encoding every LSP client must accept.
type LSPDiagnostic struct {
	Range    DiagnosticRange `json:"range"`
	Severity int             `json:"severity"`
	Code     string          `json:"code"`
	Source   string          `json:"source"`
	Message  string          `json:"message"`
}

// DiagnosticFinding is one diagnostic as the report projects it: its UTF-8
// span, and the LSP range of that span when the span lies in the text.
type DiagnosticFinding struct {
	Code string `json:"code"`
	// File is the diagnostic's file relative to the module root, for a
	// program read from a directory (Diagnostic.File); omitted otherwise.
	File              string         `json:"file,omitempty"`
	Origin            string         `json:"origin"`
	Severity          string         `json:"severity"`
	Message           string         `json:"message"`
	Span              Span           `json:"span"`
	LocationAvailable bool           `json:"locationAvailable"`
	LSP               *LSPDiagnostic `json:"lsp,omitempty"`
}

// DiagnosticCounts counts findings by severity.
type DiagnosticCounts struct {
	Errors      int `json:"errors"`
	Warnings    int `json:"warnings"`
	Information int `json:"information"`
	Hints       int `json:"hints"`
}

// DiagnosticReport is what `ergo diagnostics --json` prints. TotalCounts
// describes the complete finding set.
type DiagnosticReport struct {
	ProducerMetadata
	SchemaVersion int                 `json:"schemaVersion"`
	Source        SourceIdentity      `json:"source"`
	Revision      string              `json:"revision"`
	Target        string              `json:"target"`
	Checked       bool                `json:"checked"`
	Strict        bool                `json:"strict"`
	PolicyPassed  bool                `json:"policyPassed"`
	TotalCounts   DiagnosticCounts    `json:"totalCounts"`
	Diagnostics   []DiagnosticFinding `json:"diagnostics"`
	ReturnedCount int                 `json:"returnedCount"`
	Truncated     bool                `json:"truncated"`
}

// Report projects the result's diagnostics onto the snapshot's text. Every
// diagnostic is an error, so the policy passes exactly when the source
// checked; strict mode is recorded and changes nothing until a diagnostic
// can be a warning. Identical findings are reported once, in source order;
// a finding without a location comes last and is projected to the document
// start for LSP, so an editor shows it instead of dropping it.
func (r *Result) Report(snapshot SourceSnapshot, strict bool) DiagnosticReport {
	if snapshot.Origin == "" {
		snapshot.Origin = "disk"
	}
	if snapshot.URI == "" {
		snapshot.URI = "<buffer>"
	}
	report := DiagnosticReport{
		ProducerMetadata: r.producer,
		SchemaVersion:    DiagnosticReportSchemaVersion,
		Source:           SourceIdentity{URI: snapshot.URI, Origin: snapshot.Origin},
		Revision:         r.Revision,
		Target:           r.Target,
		Checked:          r.Checked,
		Strict:           strict,
		PolicyPassed:     r.Checked,
		Diagnostics:      []DiagnosticFinding{},
	}
	indexes := map[string]sourcePositionIndex{}
	textOf := func(file string) (string, sourcePositionIndex) {
		text := snapshot.Text
		if file != "" {
			text = snapshot.Files[file]
		}
		index, ok := indexes[file]
		if !ok {
			index = newSourcePositionIndex(text)
			indexes[file] = index
		}
		return text, index
	}
	seen := map[string]bool{}
	for _, diagnostic := range r.Diagnostics {
		span := diagnostic.Span
		key := strings.Join([]string{diagnostic.Code, diagnostic.File, diagnostic.Message, fmt.Sprintf("%d:%d", span.Offset, span.Length)}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		finding := DiagnosticFinding{Code: diagnostic.Code, File: diagnostic.File, Origin: "compiler", Severity: "error", Message: diagnostic.Message, Span: span}
		lsp := &LSPDiagnostic{Severity: 1, Code: diagnostic.Code, Source: "ergo", Message: diagnostic.Message}
		// A zero Span is the no-location value, such as the EF110 refusal.
		// A real span carries one-based coordinates, also when it is empty.
		text, index := textOf(diagnostic.File)
		if _, known := snapshot.Files[diagnostic.File]; diagnostic.File != "" && !known {
			span = Span{} // a file the snapshot does not hold has no location here
		}
		if location, ok := index.rangeFor(text, span); span != (Span{}) && ok {
			finding.LocationAvailable = true
			lsp.Range = location
		}
		finding.LSP = lsp
		report.Diagnostics = append(report.Diagnostics, finding)
	}
	sort.SliceStable(report.Diagnostics, func(i, j int) bool {
		left, right := report.Diagnostics[i], report.Diagnostics[j]
		if left.LocationAvailable != right.LocationAvailable {
			return left.LocationAvailable
		}
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Span.Offset != right.Span.Offset {
			return left.Span.Offset < right.Span.Offset
		}
		if left.Span.Length != right.Span.Length {
			return left.Span.Length < right.Span.Length
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		return left.Message < right.Message
	})
	report.TotalCounts.Errors = len(report.Diagnostics)
	report.ReturnedCount = len(report.Diagnostics)
	return report
}

// WriteText renders the report for a terminal, naming the source as source:
// one line per finding at the one-based UTF-16 line and column of its LSP
// range (the document start for a finding without a location), and
// `source: no diagnostics` when there is none. A finding with a File names
// that file (relative to the module root) in place of source.
func (r DiagnosticReport) WriteText(w io.Writer, source string) error {
	if len(r.Diagnostics) == 0 {
		_, err := fmt.Fprintf(w, "%s: no diagnostics\n", source)
		return err
	}
	for _, finding := range r.Diagnostics {
		start := finding.LSP.Range.Start
		name := source
		if finding.File != "" {
			name = finding.File
		}
		if _, err := fmt.Fprintf(w, "%s:%d:%d: %s %s: %s\n", name, start.Line+1, start.Character+1, finding.Severity, finding.Code, finding.Message); err != nil {
			return err
		}
	}
	return nil
}

// sourcePositionIndex maps every UTF-8 byte offset of a text to its LSP
// position. An offset inside a UTF-8 sequence, or between the CR and LF of
// a CRLF, has none. LF, CRLF and a lone CR each end a line.
type sourcePositionIndex struct {
	positions []DiagnosticPosition
	valid     []bool
}

func newSourcePositionIndex(source string) sourcePositionIndex {
	index := sourcePositionIndex{
		positions: make([]DiagnosticPosition, len(source)+1),
		valid:     make([]bool, len(source)+1),
	}
	line, character := 0, 0
	index.valid[0] = true
	for offset := 0; offset < len(source); {
		if source[offset] == '\r' && offset+1 < len(source) && source[offset+1] == '\n' {
			offset += 2
			line++
			character = 0
			index.positions[offset] = DiagnosticPosition{Line: line}
			index.valid[offset] = true
			continue
		}
		if source[offset] == '\n' || source[offset] == '\r' {
			offset++
			line++
			character = 0
			index.positions[offset] = DiagnosticPosition{Line: line}
			index.valid[offset] = true
			continue
		}
		runeValue, width := utf8.DecodeRuneInString(source[offset:])
		offset += width
		if runeValue > 0xffff {
			character += 2
		} else {
			character++
		}
		index.positions[offset] = DiagnosticPosition{Line: line, Character: character}
		index.valid[offset] = true
	}
	return index
}

// position is the LSP position of a byte offset. An offset between the CR
// and LF of a CRLF, where a lexer span may end, has no LSP position; it is
// projected to the line end before the CR, and the finding keeps its
// original byte span.
func (index sourcePositionIndex) position(source string, offset int) (DiagnosticPosition, bool) {
	if index.valid[offset] {
		return index.positions[offset], true
	}
	if offset > 0 && offset < len(source) && source[offset-1] == '\r' && source[offset] == '\n' && index.valid[offset-1] {
		return index.positions[offset-1], true
	}
	return DiagnosticPosition{}, false
}

// rangeFor maps a UTF-8 byte span to an end-exclusive LSP range. It fails
// when the span leaves the text or an end lands inside a UTF-8 sequence.
func (index sourcePositionIndex) rangeFor(source string, span Span) (DiagnosticRange, bool) {
	if span.Offset < 0 || span.Length < 0 || span.Offset > len(source) || span.Length > len(source)-span.Offset {
		return DiagnosticRange{}, false
	}
	start, startOK := index.position(source, span.Offset)
	end, endOK := index.position(source, span.Offset+span.Length)
	if !startOK || !endOK {
		return DiagnosticRange{}, false
	}
	return DiagnosticRange{Start: start, End: end}, true
}
