package lint

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// ProtocolVersion is the rule-pack process protocol version. It versions
// the framing and the Request/Response shapes; the snapshot inside a
// request carries its own fact schema version.
//
// A pack process reads exactly one request frame from stdin, which the
// runner then closes, writes exactly one response frame to stdout and exits
// 0. A frame is the ASCII header "ERGO-LINT <version> <length>\n" followed
// by exactly length bytes of JSON. Decimal numbers have no sign or leading
// zero; the header is at most 64 bytes. stderr is free-form diagnostics.
// A pack that does not speak the request's version answers with a frame of
// its own version, which the runner reports as a protocol mismatch.
const ProtocolVersion = 1

const (
	frameMagic          = "ERGO-LINT"
	maxFrameHeaderBytes = 64
	// MaxRequestBytes bounds the request payload a pack accepts.
	MaxRequestBytes = 256 << 20
)

// Request is one protocol request: the selected, admitted rules of one pack
// with validated options over one fact snapshot. ID is an opaque
// commitment to the rest of the request and to the execution answering it
// (see bind); a response must echo it and the snapshot revision.
type Request struct {
	ID       string          `json:"id"`
	Pack     RequestPack     `json:"pack"`
	Rules    []RequestRule   `json:"rules"`
	Snapshot json.RawMessage `json:"snapshot"`
}

// RequestPack identifies the manifest the request was admitted under.
type RequestPack struct {
	Namespace string `json:"namespace"`
	Version   string `json:"version"`
	Identity  string `json:"identity"`
}

// RequestRule is one rule to run, by local name and manifest version, with
// effective options (defaults applied).
type RequestRule struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Options json.RawMessage `json:"options"`
}

// Response answers one request with exactly one result per requested rule.
type Response struct {
	ID       string       `json:"id"`
	Revision string       `json:"revision"`
	Rules    []RuleResult `json:"rules"`
}

// RuleResult is one rule's outcome inside a pack: completed with findings,
// or failed with a reason.
type RuleResult struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Reason   string    `json:"reason,omitempty"`
	Findings []Finding `json:"findings,omitempty"`
}

// Main serves pack over the process's stdin and stdout and exits. A pack's
// main function is a single call:
//
//	func main() { lint.Main(mypack.Pack) }
func Main(pack *Pack) {
	if err := Serve(pack, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "lint pack:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Serve answers one request frame read from in with one response frame
// written to out. Rule errors and panics become failed rule results; only
// an unreadable request is an error.
func Serve(pack *Pack, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	version, length, problem := readFrameHeader(reader)
	if problem != nil {
		return errors.New(problem.message)
	}
	if version != ProtocolVersion {
		refusal, _ := json.Marshal(map[string]string{"error": fmt.Sprintf("unsupported protocol version %d; this pack speaks %d", version, ProtocolVersion)})
		if err := writeFrame(out, refusal); err != nil {
			return err
		}
		return fmt.Errorf("unsupported protocol version %d", version)
	}
	if length > MaxRequestBytes {
		return fmt.Errorf("request of %d bytes exceeds %d", length, MaxRequestBytes)
	}
	payload, problem := readFramePayload(reader, length)
	if problem != nil {
		return errors.New(problem.message)
	}
	var request Request
	if err := decodeStrict(payload, &request); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	response, err := json.Marshal(respond(pack, request))
	if err != nil {
		return err
	}
	return writeFrame(out, response)
}

// respond runs the requested rules of pack. It is the one rule execution
// path behind both the process protocol and in-process Evaluate. Each rule
// reads a private copy decoded from the request's snapshot bytes, so a
// rule cannot alter what another rule sees.
func respond(pack *Pack, request Request) Response {
	var header struct {
		Schema   Schema   `json:"schema"`
		Semantic Semantic `json:"semantic"`
	}
	headerErr := json.Unmarshal(request.Snapshot, &header)
	response := Response{ID: request.ID, Revision: header.Semantic.Revision, Rules: []RuleResult{}}
	for _, requested := range request.Rules {
		result := RuleResult{Name: requested.Name, Status: StatusFailed}
		rule := pack.Rule(requested.Name)
		switch {
		case request.Pack.Namespace != pack.Namespace:
			result.Reason = fmt.Sprintf("request is for pack %q; this pack is %q", request.Pack.Namespace, pack.Namespace)
		case rule == nil:
			result.Reason = "pack does not implement its manifest rule " + requested.Name
		case rule.Version != requested.Version:
			result.Reason = fmt.Sprintf("manifest declares rule version %s; the pack implements %s", requested.Version, rule.Version)
		case headerErr != nil:
			result.Reason = "decode fact snapshot: " + headerErr.Error()
		case header.Schema.Name != FactSchemaName || !slices.Contains(pack.FactVersions, header.Schema.Version):
			result.Reason = fmt.Sprintf("pack does not read fact schema %s/%d", header.Schema.Name, header.Schema.Version)
		default:
			result.Status, result.Reason, result.Findings = runRule(rule, requested.Options, request.Snapshot)
		}
		response.Rules = append(response.Rules, result)
	}
	return response
}

func runRule(rule *Rule, rawOptions json.RawMessage, snapshot json.RawMessage) (string, string, []Finding) {
	options, err := ValidateOptions(rule.Options, rawOptions)
	if err != nil {
		return StatusFailed, "options: " + err.Error(), nil
	}
	view := &Snapshot{}
	if err := json.Unmarshal(snapshot, view); err != nil {
		return StatusFailed, "decode fact snapshot: " + err.Error(), nil
	}
	findings, err := Apply(rule, view, options)
	if err != nil {
		return StatusFailed, err.Error(), nil
	}
	return StatusCompleted, "", findings
}

// execution is one pack's admitted work over one snapshot: the report with
// statuses decided before running (off, skipped, failed admission) and the
// request for the rules that may run. Responses are validated against the
// source and revision copied here at admission, never against state a rule
// or pack can write.
type execution struct {
	namespace string
	source    *sourceBounds
	revision  string
	settings  map[string]RuleSetting
	request   Request
	report    Report
}

// prepare admits every manifest rule of the selected pack namespace under
// configuration. It runs no pack code.
func prepare(configuration *Configuration, namespace string, snapshot *Snapshot) (*execution, error) {
	manifest, ok := configuration.registry.Pack(namespace)
	if !ok {
		return nil, fmt.Errorf("rule pack %s is not selected by this configuration", namespace)
	}
	wire, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode fact snapshot: %w", err)
	}
	work := &execution{
		namespace: namespace,
		source:    newSourceBounds(snapshot.Sources),
		revision:  snapshot.Semantic.Revision,
		settings:  map[string]RuleSetting{},
		request: Request{
			Pack:     RequestPack{Namespace: namespace, Version: manifest.Version, Identity: manifest.Identity()},
			Rules:    []RequestRule{},
			Snapshot: wire,
		},
		report: Report{Complete: true, Rules: []RuleStatus{}, Findings: []ReportedFinding{}},
	}
	for _, metadata := range manifest.Rules {
		id := namespace + "/" + metadata.Name
		setting, _ := configuration.Setting(id)
		if setting.Severity == SeverityOff {
			work.report.Rules = append(work.report.Rules, RuleStatus{Rule: id, Status: StatusOff})
			continue
		}
		if refusal := admit(manifest, metadata, setting, snapshot); refusal.Status != "" {
			refusal.Rule = id
			work.report.Rules = append(work.report.Rules, refusal)
			continue
		}
		work.settings[metadata.Name] = setting
		work.request.Rules = append(work.request.Rules, RequestRule{Name: metadata.Name, Version: metadata.Version, Options: setting.Options.canonical()})
	}
	work.report.Analysis = configuration.analysis(snapshot, wire).requested(work.request)
	return work, nil
}

// bind commits the request ID to the request and to what answers it: a
// pack process qualified by execution, or, for nil, the in-process
// authoring harness. The ID digests a domain-separated envelope of both,
// so an answer computed under another executable or environment cannot
// bind to this request. Packs echo it as an opaque value.
func (e *execution) bind(execution *ExecutionIdentity) {
	domain := "ergo.lint.request/in-process"
	if execution != nil {
		domain = "ergo.lint.request/process"
		e.report.Analysis = e.report.Analysis.executed(*execution)
	} else {
		e.report.Analysis = e.report.Analysis.inProcess()
	}
	e.request.ID = ""
	e.request.ID = digest(map[string]any{"domain": domain, "request": e.request, "execution": execution})
}

// accept validates an untrusted response, whose serialized payload is size
// bytes, against the request. A response that is not an answer to exactly
// this request, or exceeds a limit, fails the whole pack; an invalid result
// of one rule fails that rule only. Findings are never truncated or
// partially accepted.
func (e *execution) accept(response Response, size int, limits Limits) *ExecutionFailure {
	fail := func(code, format string, args ...any) *ExecutionFailure {
		return &ExecutionFailure{Pack: e.namespace, Code: code, Message: fmt.Sprintf(format, args...)}
	}
	if size > limits.MaxResponseBytes {
		return fail(FailureOversized, "%s", oversizedResponse(size, limits.MaxResponseBytes))
	}
	if response.Revision != e.revision {
		return fail(FailureStale, "response is for snapshot revision %q; the request carried %q", response.Revision, e.revision)
	}
	if response.ID != e.request.ID {
		return fail(FailureInvalid, "response answers request %q, not %q", response.ID, e.request.ID)
	}
	results := map[string]RuleResult{}
	total := 0
	for _, result := range response.Rules {
		if _, requested := e.settings[result.Name]; !requested {
			return fail(FailureInvalid, "response names rule %q, which was not requested", result.Name)
		}
		if _, duplicate := results[result.Name]; duplicate {
			return fail(FailureInvalid, "response repeats rule %q", result.Name)
		}
		switch result.Status {
		case StatusCompleted:
		case StatusFailed:
			if validateMessage(result.Reason) != nil || len(result.Findings) > 0 {
				return fail(FailureInvalid, "failed rule %q needs a reason of at most %d bytes and no findings", result.Name, MaxMessageBytes)
			}
		default:
			return fail(FailureInvalid, "rule %q has status %q; a pack reports completed or failed", result.Name, result.Status)
		}
		results[result.Name] = result
		total += len(result.Findings)
	}
	if total > limits.MaxFindings {
		return fail(FailureOversized, "response carries %d findings; the limit is %d", total, limits.MaxFindings)
	}
	// The response must answer exactly the requested set. Every pack-level
	// refusal is decided here, before any result is recorded.
	for _, requested := range e.request.Rules {
		if _, ok := results[requested.Name]; !ok {
			return fail(FailureInvalid, "response has no result for rule %q", requested.Name)
		}
	}
	for _, requested := range e.request.Rules {
		result := results[requested.Name]
		id := e.namespace + "/" + requested.Name
		status := RuleStatus{Rule: id, Status: result.Status, Reason: result.Reason}
		if result.Status == StatusCompleted {
			if reason := validateFindings(result.Findings, e.source); reason != "" {
				status.Status, status.Reason = StatusFailed, "invalid output: "+reason
			} else {
				status.Findings = len(result.Findings)
				severity := e.settings[requested.Name].Severity
				for _, finding := range result.Findings {
					e.report.Findings = append(e.report.Findings, ReportedFinding{Rule: id, Severity: severity, Message: finding.Message, Span: finding.Span, Related: finding.Related, Suggestions: finding.Suggestions})
				}
			}
		}
		e.report.Rules = append(e.report.Rules, status)
	}
	return nil
}

func validateFindings(findings []Finding, source *sourceBounds) string {
	if len(findings) > MaxFindingsPerRule {
		return fmt.Sprintf("rule reported %d findings; the limit is %d", len(findings), MaxFindingsPerRule)
	}
	for _, finding := range findings {
		if err := validateFinding(finding, source); err != nil {
			return err.Error()
		}
	}
	return ""
}

// fail records a pack-level failure: every requested rule failed with it
// and no finding of the pack is reported. It is only called on an execution
// with no recorded result: accept decides every pack-level refusal, including
// an incomplete rule set, before it records any result.
func (e *execution) fail(failure *ExecutionFailure) {
	e.report.Failure = failure
	for _, requested := range e.request.Rules {
		e.report.Rules = append(e.report.Rules, RuleStatus{Rule: e.namespace + "/" + requested.Name, Status: StatusFailed, Reason: "pack-failed: " + failure.Code})
	}
}

func (e *execution) finish() Report {
	report := e.report
	for _, status := range report.Rules {
		if status.Status == StatusFailed || status.Status == StatusSkipped && !status.Inapplicable {
			report.Complete = false
		}
	}
	slices.SortFunc(report.Rules, func(a, b RuleStatus) int { return strings.Compare(a.Rule, b.Rule) })
	slices.SortStableFunc(report.Findings, compareFindings)
	return report
}

// frameProblem is a framing failure. Content problems are visible in the
// bytes already read; the others mean the stream ended early, which a
// crash explains better.
type frameProblem struct {
	code    string
	message string
	content bool
}

func writeFrame(out io.Writer, payload []byte) error {
	frame := make([]byte, 0, len(payload)+maxFrameHeaderBytes)
	frame = fmt.Appendf(frame, "%s %d %d\n", frameMagic, ProtocolVersion, len(payload))
	_, err := out.Write(append(frame, payload...))
	return err
}

func readFrameHeader(reader *bufio.Reader) (int, int, *frameProblem) {
	header := make([]byte, 0, maxFrameHeaderBytes)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if len(header) == 0 {
				return 0, 0, &frameProblem{code: FailureMalformed, message: "no frame: " + describeEnd(err)}
			}
			return 0, 0, &frameProblem{code: FailureMalformed, message: "truncated frame header: " + describeEnd(err)}
		}
		if b == '\n' {
			break
		}
		if len(header) == maxFrameHeaderBytes {
			return 0, 0, &frameProblem{code: FailureMalformed, message: fmt.Sprintf("frame header exceeds %d bytes", maxFrameHeaderBytes), content: true}
		}
		header = append(header, b)
	}
	fields := strings.Split(string(header), " ")
	malformed := &frameProblem{code: FailureMalformed, message: fmt.Sprintf("malformed frame header %q", header), content: true}
	if len(fields) != 3 || fields[0] != frameMagic {
		return 0, 0, malformed
	}
	version, versionOK := parseDecimal(fields[1])
	length, lengthOK := parseDecimal(fields[2])
	if !versionOK || !lengthOK || version == 0 {
		return 0, 0, malformed
	}
	return version, length, nil
}

func readFramePayload(reader *bufio.Reader, length int) ([]byte, *frameProblem) {
	payload := make([]byte, length)
	if read, err := io.ReadFull(reader, payload); err != nil {
		return nil, &frameProblem{code: FailureMalformed, message: fmt.Sprintf("frame truncated after %d of %d bytes: %s", read, length, describeEnd(err))}
	}
	switch _, err := reader.ReadByte(); {
	case err == nil:
		return nil, &frameProblem{code: FailureMalformed, message: "data follows the frame", content: true}
	case err != io.EOF:
		return nil, &frameProblem{code: FailureMalformed, message: "stream did not end after the frame: " + err.Error(), content: true}
	}
	return payload, nil
}

func describeEnd(err error) string {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return "end of output"
	}
	return err.Error()
}

// parseDecimal accepts a canonical non-negative decimal: no sign, no
// leading zero, at most 12 digits.
func parseDecimal(text string) (int, bool) {
	if text == "" || len(text) > 12 || (len(text) > 1 && text[0] == '0') || strings.TrimLeft(text, "0123456789") != "" {
		return 0, false
	}
	value, err := strconv.Atoi(text)
	return value, err == nil
}
