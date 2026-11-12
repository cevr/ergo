package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/producer"
)

func utf16Range(source string, span Span) (DiagnosticRange, bool) {
	return newSourcePositionIndex(source).rangeFor(source, span)
}

func TestUTF16RangeCountsCodeUnitsAndNormalizesCRLF(t *testing.T) {
	source := "a𐐀é\r\nz"
	start := strings.Index(source, "e")
	rangeValue, ok := utf16Range(source, Span{Offset: start, Length: len("é")})
	if !ok {
		t.Fatal("astral and combining span was rejected")
	}
	if rangeValue.Start != (DiagnosticPosition{Line: 0, Character: 3}) || rangeValue.End != (DiagnosticPosition{Line: 0, Character: 5}) {
		t.Fatalf("unexpected UTF-16 range: %+v", rangeValue)
	}
	lineStart := strings.Index(source, "z")
	lineRange, ok := utf16Range(source, Span{Offset: lineStart, Length: 1})
	if !ok || lineRange.Start != (DiagnosticPosition{Line: 1, Character: 0}) || lineRange.End != (DiagnosticPosition{Line: 1, Character: 1}) {
		t.Fatalf("unexpected CRLF range: %+v ok=%v", lineRange, ok)
	}
	if normalized, ok := utf16Range(source, Span{Offset: strings.Index(source, "\r") + 1, Length: 0}); !ok || normalized.Start != (DiagnosticPosition{Line: 0, Character: 5}) || normalized.End != normalized.Start {
		t.Fatalf("CRLF boundary was not normalized to line end: %+v ok=%v", normalized, ok)
	}
	if eof, ok := utf16Range(source, Span{Offset: len(source), Length: 0}); !ok || eof.End != (DiagnosticPosition{Line: 1, Character: 1}) {
		t.Fatalf("unexpected EOF range: %+v ok=%v", eof, ok)
	}
	lone := "a\rb"
	if r, ok := utf16Range(lone, Span{Offset: 2, Length: 1}); !ok || r.Start != (DiagnosticPosition{Line: 1, Character: 0}) {
		t.Fatalf("a lone CR does not end a line: %+v ok=%v", r, ok)
	}
	for _, span := range []Span{{Offset: start + 1, Length: 1}, {Offset: 0, Length: len(source) + 1}, {Offset: -1}, {Offset: len(source) + 1}} {
		if r, ok := utf16Range(source, span); ok {
			t.Fatalf("span %+v inside a UTF-8 sequence or outside the text projected to %+v", span, r)
		}
	}
}

func TestFileURIUsesCanonicalEscapedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space é.ergo")
	if err := os.WriteFile(path, []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	uri, err := FileURI(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "file://") || strings.Contains(uri, " ") || !strings.Contains(uri, "%20") || !strings.Contains(uri, "%C3%A9") {
		t.Fatalf("FileURI was not canonical and escaped: %q", uri)
	}
}

// located is a diagnostic located at the first occurrence of token.
func located(source, code, token string) Diagnostic {
	offset := strings.Index(source, token)
	line := strings.Count(source[:offset], "\n") + 1
	column := offset - strings.LastIndexByte(source[:offset], '\n')
	return Diagnostic{Code: code, Message: code + " message", Span: Span{Offset: offset, Length: len(token), Line: line, Column: column}}
}

func TestReportPolicyFollowsTheDiagnostics(t *testing.T) {
	source := "package p\n\nfunc main() {}\n"
	clean := NewResult(source, Target, nil)
	for _, strict := range []bool{false, true} {
		report := clean.Report(SourceSnapshot{URI: "file:///x/main.ergo", Text: source}, strict)
		if !report.Checked || !report.PolicyPassed || report.Strict != strict || len(report.Diagnostics) != 0 || report.TotalCounts != (DiagnosticCounts{}) || report.Source.Origin != "disk" {
			t.Fatalf("clean report (strict %v): %+v", strict, report)
		}
	}
	dirty := NewResult(source, Target, []Diagnostic{located(source, "S104", "main")})
	report := dirty.Report(SourceSnapshot{Text: source}, false)
	if report.Checked || report.PolicyPassed || report.TotalCounts.Errors != 1 || report.ReturnedCount != 1 || report.Source.URI != "<buffer>" {
		t.Fatalf("report of an unchecked source: %+v", report)
	}
	finding := report.Diagnostics[0]
	if finding.Origin != "compiler" || finding.Severity != "error" || !finding.LocationAvailable || finding.LSP == nil || finding.LSP.Severity != 1 || finding.LSP.Code != "S104" || finding.LSP.Source != "ergo" {
		t.Fatalf("finding %+v", finding)
	}
	if want := (DiagnosticRange{Start: DiagnosticPosition{Line: 2, Character: 5}, End: DiagnosticPosition{Line: 2, Character: 9}}); finding.LSP.Range != want {
		t.Fatalf("range %+v, want %+v", finding.LSP.Range, want)
	}
}

func TestReportProjectsALocationlessFindingToTheDocumentStart(t *testing.T) {
	source := "package p\n"
	unsupported := NewResult(source, "llvm", []Diagnostic{located(source, "S001", "p")})
	if unsupported.Checked || len(unsupported.Diagnostics) != 1 || unsupported.Diagnostics[0].Code != "EF110" {
		t.Fatalf("an unsupported target did not replace the diagnostics with EF110: %+v", unsupported)
	}
	report := unsupported.Report(SourceSnapshot{Text: source}, false)
	finding := report.Diagnostics[0]
	if report.Target != "llvm" || finding.LocationAvailable || finding.LSP == nil || finding.LSP.Range != (DiagnosticRange{}) || finding.Message != "unsupported target llvm; the only target is go" {
		t.Fatalf("EF110 fabricated a location or lost its document-start projection: %+v", finding)
	}
	outside := NewResult(source, Target, []Diagnostic{{Code: "S001", Message: "past the end", Span: Span{Offset: 99, Length: 1, Line: 9, Column: 1}}})
	if finding := outside.Report(SourceSnapshot{Text: source}, false).Diagnostics[0]; finding.LocationAvailable || finding.LSP.Range != (DiagnosticRange{}) {
		t.Fatalf("a span outside the text was located: %+v", finding)
	}
}

func TestReportProjectsCRLFSpans(t *testing.T) {
	source := "// comment\r\npackage p\r\n"
	comment := strings.Index(source, "\r\n")
	result := NewResult(source, Target, []Diagnostic{{Code: "S001", Message: "m", Span: Span{Offset: 0, Length: comment + 1, Line: 1, Column: 1}}})
	finding := result.Report(SourceSnapshot{Text: source}, false).Diagnostics[0]
	if !finding.LocationAvailable || finding.Span.Length != comment+1 {
		t.Fatalf("a span ending between CR and LF was not located: %+v", finding)
	}
	if finding.LSP.Range.Start != (DiagnosticPosition{}) || finding.LSP.Range.End != (DiagnosticPosition{Line: 0, Character: comment}) {
		t.Fatalf("unexpected CRLF range: %+v", finding.LSP.Range)
	}
}

func TestReportOrdersAndDeduplicatesFindings(t *testing.T) {
	source := "package p\n\nfunc main() { a; b }\n"
	b, a := located(source, "S104", "b"), located(source, "S104", "a")
	result := NewResult(source, Target, []Diagnostic{{Code: "S900", Message: "nowhere"}, b, a, b})
	report := result.Report(SourceSnapshot{Text: source}, false)
	var order []string
	for _, finding := range report.Diagnostics {
		order = append(order, source[finding.Span.Offset:finding.Span.Offset+finding.Span.Length]+finding.Code)
	}
	if !slices.Equal(order, []string{"aS104", "bS104", "S900"}) || report.TotalCounts.Errors != 3 || report.ReturnedCount != 3 {
		t.Fatalf("findings %v, counts %+v", order, report.TotalCounts)
	}
}

func TestReportWritesText(t *testing.T) {
	source := "package p\n\nfunc main() { 🌍; b }\n"
	var out bytes.Buffer
	if err := NewResult(source, Target, nil).Report(SourceSnapshot{Text: source}, false).WriteText(&out, "dir/main.ergo"); err != nil || out.String() != "dir/main.ergo: no diagnostics\n" {
		t.Fatalf("clean text %q (%v)", out.String(), err)
	}
	out.Reset()
	result := NewResult(source, Target, []Diagnostic{located(source, "S104", "b"), {Code: "S900", Message: "nowhere"}})
	if err := result.Report(SourceSnapshot{Text: source}, false).WriteText(&out, "main.ergo"); err != nil {
		t.Fatal(err)
	}
	want := "main.ergo:3:19: error S104: S104 message\nmain.ergo:1:1: error S900: nowhere\n"
	if out.String() != want {
		t.Fatalf("text %q, want %q (UTF-16 column after an astral character)", out.String(), want)
	}
}

func TestCheckEnvelopeTruncatesAtTheBound(t *testing.T) {
	for _, count := range []int{MaxCheckDiagnostics, MaxCheckDiagnostics + 1} {
		diagnostics := make([]Diagnostic, count)
		for i := range diagnostics {
			diagnostics[i] = Diagnostic{Code: "S104", Message: "m"}
		}
		envelope := NewResult("x", Target, diagnostics).CheckEnvelope()
		if len(envelope.Diagnostics) != MaxCheckDiagnostics || envelope.DiagnosticsTruncated != (count > MaxCheckDiagnostics) || envelope.Checked {
			t.Fatalf("%d diagnostics: listed %d, truncated %v", count, len(envelope.Diagnostics), envelope.DiagnosticsTruncated)
		}
	}
}

// TestEnvelopeShapes pins the JSON keys of the check envelope and the
// diagnostic report, and their schema versions, so a shape change is a
// deliberate version bump.
func TestEnvelopeShapes(t *testing.T) {
	source := "package p\n"
	result := NewResult(source, Target, nil)
	keys := func(value any) []string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		var names []string
		for name := range decoded {
			names = append(names, name)
		}
		slices.Sort(names)
		return names
	}
	if got, want := keys(result.CheckEnvelope()), []string{"checked", "diagnostics", "diagnosticsTruncated", "revision", "schemaVersion", "target"}; !slices.Equal(got, want) {
		t.Fatalf("unqualified envelope keys %v, want %v", got, want)
	}
	data, _ := json.Marshal(result.CheckEnvelope())
	if !bytes.Contains(data, []byte(`"diagnostics":[]`)) || !bytes.HasPrefix(data, []byte(`{"checked":true,`)) {
		t.Fatalf("envelope %s: diagnostics must be a list, keys in sorted order", data)
	}
	if err := result.Qualify(producer.Identity{Strength: "unavailable", ReuseScope: "none", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if got, want := keys(result.CheckEnvelope()), []string{"checked", "diagnostics", "diagnosticsTruncated", "producer", "revision", "schemaVersion", "snapshot", "target"}; !slices.Equal(got, want) {
		t.Fatalf("qualified envelope keys %v, want %v", got, want)
	}
	if got, want := keys(result.Report(SourceSnapshot{Text: source}, false)), []string{"checked", "diagnostics", "lintComplete", "policyPassed", "producer", "returnedCount", "revision", "schemaVersion", "snapshot", "source", "strict", "suppressions", "target", "totalCounts", "truncated"}; !slices.Equal(got, want) {
		t.Fatalf("report keys %v, want %v", got, want)
	}
	if CheckSchemaVersion != 10 || DiagnosticReportSchemaVersion != 3 || FormatterSchemaVersion != 1 {
		t.Fatal("a schema version moved without this test")
	}
}

func TestQualificationKeepsTheRevisionAndScopesTheSnapshot(t *testing.T) {
	source := "package p\n"
	result := NewResult(source, Target, nil)
	revision := result.Revision
	if envelope := result.CheckEnvelope(); envelope.Producer != (producer.Identity{}) || envelope.Snapshot != (SnapshotQualification{}) {
		t.Fatal("an unqualified result carries a producer")
	}
	id := producer.Identity{Strength: "executing-artifact", ReuseScope: "artifact", Digest: "sha256:" + strings.Repeat("a", 64), Qualifier: "sha256:" + strings.Repeat("a", 64)}
	if err := result.Qualify(id); err != nil {
		t.Fatal(err)
	}
	want := SnapshotQualification{SchemaVersion: CheckSchemaVersion, Revision: revision, Target: Target, Producer: id.Qualifier, ReuseScope: "artifact"}
	envelope := result.CheckEnvelope()
	report := result.Report(SourceSnapshot{Text: source}, false)
	if result.Revision != revision || envelope.Producer != id || envelope.Snapshot != want || report.Producer != id || report.Snapshot != want {
		t.Fatalf("qualification lost or rewrote facts: %+v / %+v", envelope, report.ProducerMetadata)
	}
}

func TestQualificationAdmitsOnlyWellFormedBoundedIdentities(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	valid := []producer.Identity{
		{Strength: "executing-artifact", ReuseScope: "artifact", Digest: digest, Qualifier: digest},
		{Strength: "unavailable", ReuseScope: "none", Reason: "image unavailable"},
		{Strength: "unavailable", ReuseScope: "process", Qualifier: "process:ours", Reason: "image unavailable"},
	}
	for _, id := range valid {
		if err := NewResult("x", Target, nil).Qualify(id); err != nil {
			t.Fatalf("%+v refused: %v", id, err)
		}
	}
	long := valid[1]
	long.Declaration.Module = strings.Repeat("x", maxProducerField+1)
	invalid := []producer.Identity{
		{},
		{Strength: "executing-artifact", ReuseScope: "artifact", Digest: digest, Qualifier: "sha256:" + strings.Repeat("b", 64)},
		{Strength: "executing-artifact", ReuseScope: "process", Digest: digest, Qualifier: digest},
		{Strength: "executing-artifact", ReuseScope: "artifact", Digest: "sha256:abc", Qualifier: "sha256:abc"},
		{Strength: "unavailable", ReuseScope: "process", Qualifier: "process:", Reason: "r"},
		{Strength: "unavailable", ReuseScope: "none"},
		long,
	}
	for _, id := range invalid {
		result := NewResult("x", Target, nil)
		if err := result.Qualify(id); err == nil {
			t.Fatalf("%+v admitted", id)
		}
		if result.CheckEnvelope().Producer != (producer.Identity{}) {
			t.Fatalf("a refused identity qualified the result: %+v", id)
		}
	}
}

func TestFormatErrorsAndDigest(t *testing.T) {
	if FormatDigest("") != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal("FormatDigest is not the hex SHA-256 of the bytes")
	}
	var err error = FormatFailure{Diagnostics: []Diagnostic{{Code: "S001", Message: "unexpected )"}}}
	var failure FormatFailure
	if !errors.As(err, &failure) || err.Error() != "source cannot be formatted: unexpected )" || (FormatFailure{}).Error() != "source cannot be formatted" {
		t.Fatalf("failure text %q", err)
	}
	err = FormatLimitError{Limit: 10}
	var limit FormatLimitError
	if !errors.As(err, &limit) || limit.Limit != 10 || err.Error() != "formatted source exceeds 10-byte limit" {
		t.Fatalf("limit text %q", err)
	}
}
