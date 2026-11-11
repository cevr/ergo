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
	"strings"

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

// UnsupportedPlatform returns the EF110 diagnostic for a platform (GOOS/GOARCH)
// outside platforms, the target list, which it names: a platform is part
// of the target, so it has the target's refusal.
func UnsupportedPlatform(platform string, platforms []string) Diagnostic {
	return Diagnostic{Code: "EF110", Message: "unsupported target " + Target + " on " + platform + "; the platforms are " + strings.Join(platforms, ", ")}
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
	evaluation  *Evaluation
}

// NewResult is the result of source under the given diagnostics. An
// unsupported target replaces them with the EF110 refusal, which has no
// location. Checked means only that no diagnostic remains.
func NewResult(source, target string, diagnostics []Diagnostic) *Result {
	r := &Result{SchemaVersion: CheckSchemaVersion, Revision: Revision(source), Target: target, Diagnostics: append([]Diagnostic{}, diagnostics...)}
	if diagnostic, unsupported := UnsupportedTarget(target); unsupported {
		r.Diagnostics = []Diagnostic{diagnostic}
	}
	r.Checked = len(r.Diagnostics) == 0
	return r
}

// EvaluationSchemaVersion versions the evaluation object of the check
// envelope (L7, check-time evaluation).
const EvaluationSchemaVersion = 1

// Evaluation is what `ergo check` reports of running a checked source's
// pure obligations (its tests, then its law runs) with no Go build. It is
// apart from Checked: a source checks or not before anything runs.
// Revision is the source the evaluation ran on. Status sums the
// obligations up, worst first: "defect", "refuted", "incomplete" (a budget
// ran out, so some obligation did not finish), "skipped" (some obligation
// cannot run at check time), "passed" (every one ran and held), and "none"
// (there is no obligation). A skipped or incomplete obligation is never
// passed. Steps and Bytes are the work and value bytes used, of their
// limits.
type Evaluation struct {
	Bytes         int64        `json:"bytes"`
	BytesLimit    int64        `json:"bytesLimit"`
	Obligations   []Obligation `json:"obligations"`
	Revision      string       `json:"revision"`
	SchemaVersion int          `json:"schemaVersion"`
	Status        string       `json:"status"`
	Steps         int64        `json:"steps"`
	StepsLimit    int64        `json:"stepsLimit"`
}

// Obligation is one test or law run of an evaluation, in the order ergo
// test runs them. Status is "passed", "refuted", "defect", "skipped",
// "incomplete" or "not run"; Reason says why for each but passed. Evidence
// is "evaluated" (an example), "tested" (sampled cases) or "proved" (a
// whole finite domain, or an exhausted machine graph). Case is the first
// bad case and Arguments its values; Replay is the native command that
// runs the same cases (ergo test draws the same ones for the same seed).
type Obligation struct {
	Arguments   []Argument     `json:"arguments,omitempty"`
	Case        int            `json:"case,omitempty"`
	Cases       int            `json:"cases"`
	Evidence    string         `json:"evidence,omitempty"`
	Machine     *MachineSearch `json:"machine,omitempty"`
	Name        string         `json:"name"`
	Reason      string         `json:"reason,omitempty"`
	Replay      string         `json:"replay,omitempty"`
	Seed        uint64         `json:"seed,omitempty"`
	Site        Site           `json:"site"`
	Declaration *Site          `json:"declaration,omitempty"`
	Status      string         `json:"status"`
	Steps       int64          `json:"steps"`
	TotalCases  int            `json:"totalCases,omitempty"`
}

// Argument is one argument of an obligation's first bad case.
type Argument struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Site is a one-based line and byte column; File is the file's name in a
// package folder, empty for one source file.
type Site struct {
	Column int    `json:"column"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line"`
}

// MachineSearch is a machine reachability law's search: the states,
// edges and depth it reached, the step calls it made (replays too), and
// whether it listed the whole graph. Runs counts the generated runs when
// it could not, and Run is the run that decided the law.
type MachineSearch struct {
	Checked  int    `json:"checked"`
	Complete bool   `json:"complete"`
	Depth    int    `json:"depth"`
	Edges    int    `json:"edges"`
	Run      string `json:"run,omitempty"`
	Runs     int    `json:"runs,omitempty"`
	States   int    `json:"states"`
	Steps    int64  `json:"steps"`
}

// EvaluationStatus sums obligation statuses up, worst first (Evaluation).
func EvaluationStatus(statuses []string) string {
	rank := map[string]int{"passed": 1, "skipped": 2, "incomplete": 3, "not run": 3, "refuted": 4, "defect": 5}
	names := []string{"none", "passed", "skipped", "incomplete", "refuted", "defect"}
	worst := 0
	for _, s := range statuses {
		r, ok := rank[s]
		if !ok {
			r = len(names) - 1 // an unknown status is never read as a pass
		}
		worst = max(worst, r)
	}
	return names[worst]
}

// Revision identifies a source text: the SHA-256 of its bytes, in hex.
func Revision(source string) string {
	hash := sha256.Sum256([]byte(source))
	return hex.EncodeToString(hash[:])
}

// SetEvaluation attaches e to the result's check envelope. e keeps its
// own Revision, the text it ran on, so a reader sees that it ran on this
// result's source.
func (r *Result) SetEvaluation(e Evaluation) {
	e.SchemaVersion = EvaluationSchemaVersion
	r.evaluation = &e
}

// Evaluation is the attached evaluation, nil when none ran.
func (r *Result) Evaluation() *Evaluation { return r.evaluation }

// CheckEnvelope is what `ergo check` prints. Its fields are declared in
// key order, so the JSON matches the sorted-key map it replaced.
// Evaluation is present only when the source checked and its obligations
// ran (Evaluation).
type CheckEnvelope struct {
	Checked              bool                  `json:"checked"`
	Diagnostics          []Diagnostic          `json:"diagnostics"`
	DiagnosticsTruncated bool                  `json:"diagnosticsTruncated"`
	Evaluation           *Evaluation           `json:"evaluation,omitempty"`
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
		Evaluation:           r.evaluation,
		Producer:             r.producer.Producer,
		Revision:             r.Revision,
		SchemaVersion:        r.SchemaVersion,
		Snapshot:             r.producer.Snapshot,
		Target:               r.Target,
	}
}
