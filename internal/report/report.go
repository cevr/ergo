// Package report owns the diagnostics-only result of one checked source and
// every projection the CLI prints from it: the check envelope, the
// diagnostic report with UTF-8 byte spans and UTF-16 LSP ranges, its text
// rendering, producer qualification, the formatter's result types and the
// EF110 refusal of a target other than Go. It knows nothing of a front end:
// the front end hands it diagnostics with byte spans, and it projects them.
package report

import (
	"crypto/sha256"
	"encoding/hex"

	"ergo.local/prototype/internal/producer"
)

// CheckSchemaVersion versions the check envelope and the snapshot
// qualification every qualified projection carries. Version 10 dropped the
// edition field (#205).
const CheckSchemaVersion = 10

// MaxCheckDiagnostics bounds the diagnostics a check envelope lists. The
// envelope says when it dropped the rest.
const MaxCheckDiagnostics = 100

// Span is a UTF-8 byte span into one source text, with the one-based line
// and byte column of its start. The zero Span is the explicit no-location
// value; a real span carries a line of at least 1, also when it is empty at
// the end of the text.
type Span struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
	Line   int `json:"line"`
	Column int `json:"column"`
}

// Diagnostic is one finding of the front end: a registered code, a message
// and where it applies. File is the slash-separated path of the source file
// the span lies in, relative to the module root, for a program read from a
// directory (196b Q10); it is empty for a program of one named file, whose
// spans lie in that file, and for a diagnostic with no location. The JSON
// field is additive: omitted when empty, and in key order.
type Diagnostic struct {
	Code    string `json:"code"`
	File    string `json:"file,omitempty"`
	Message string `json:"message"`
	Span    Span   `json:"span"`
}

// Target is Ergo's only compilation target.
const Target = "go"

// UnsupportedTarget returns the EF110 diagnostic for any target other than
// Target. Every command that accepts a target refuses it with this text.
func UnsupportedTarget(target string) (Diagnostic, bool) {
	if target == Target {
		return Diagnostic{}, false
	}
	return Diagnostic{Code: "EF110", Message: "unsupported target " + target + "; the only target is " + Target}, true
}

// Result is the diagnostics-only result of one source text: its revision,
// the target, the diagnostics, and whether there are none. It carries no
// program, so it states no checked facts beyond the diagnostics.
type Result struct {
	SchemaVersion int
	// Revision is the SHA-256 of the source bytes, in hex.
	Revision    string
	Target      string
	Checked     bool
	Diagnostics []Diagnostic
	producer    ProducerMetadata
}

// NewResult is the result of source under the given diagnostics. An
// unsupported target replaces them with the EF110 refusal, which has no
// location. Checked means only that no diagnostic remains.
func NewResult(source, target string, diagnostics []Diagnostic) *Result {
	hash := sha256.Sum256([]byte(source))
	r := &Result{SchemaVersion: CheckSchemaVersion, Revision: hex.EncodeToString(hash[:]), Target: target, Diagnostics: append([]Diagnostic{}, diagnostics...)}
	if diagnostic, unsupported := UnsupportedTarget(target); unsupported {
		r.Diagnostics = []Diagnostic{diagnostic}
	}
	r.Checked = len(r.Diagnostics) == 0
	return r
}

// CheckEnvelope is what `ergo check` prints. Its fields are declared in
// key order, so the JSON matches the sorted-key map it replaced.
type CheckEnvelope struct {
	Checked              bool                  `json:"checked"`
	Diagnostics          []Diagnostic          `json:"diagnostics"`
	DiagnosticsTruncated bool                  `json:"diagnosticsTruncated"`
	Producer             producer.Identity     `json:"producer,omitzero"`
	Revision             string                `json:"revision"`
	SchemaVersion        int                   `json:"schemaVersion"`
	Snapshot             SnapshotQualification `json:"snapshot,omitzero"`
	Target               string                `json:"target"`
}

// CheckEnvelope lists at most MaxCheckDiagnostics diagnostics and says when
// it dropped the rest. It carries the producer only once the result is
// qualified.
func (r *Result) CheckEnvelope() CheckEnvelope {
	diagnostics := r.Diagnostics
	truncated := len(diagnostics) > MaxCheckDiagnostics
	if truncated {
		diagnostics = diagnostics[:MaxCheckDiagnostics]
	}
	return CheckEnvelope{
		Checked:              r.Checked,
		Diagnostics:          append([]Diagnostic{}, diagnostics...),
		DiagnosticsTruncated: truncated,
		Producer:             r.producer.Producer,
		Revision:             r.Revision,
		SchemaVersion:        r.SchemaVersion,
		Snapshot:             r.producer.Snapshot,
		Target:               r.Target,
	}
}
