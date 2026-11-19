package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdLintLawsProbe restates the laws of std lint's laws.ergo as Ergo
// property tests over the package's exported API.
const stdLintLawsProbe = `import json
import lint
import strings

test LintSnapshotRoundTrips(s lint.Snapshot) {
	let back = lint.DecodeSnapshot(lint.EncodeSnapshot(s)) catch {
	case lint.SnapshotFailed:
		assert false
		s
	}
	assert back == s
}

test LintFrameRoundTrips(payload string) {
	let f = lint.ReadFrame(lint.WriteFrame(payload)) catch {
	case lint.FrameMalformed:
		assert false
		lint.Frame{Version: 0, Payload: ""}
	}
	assert f == lint.Frame{Version: lint.ProtocolVersion, Payload: payload}
}

test LintFrameIsExact(payload string, extra string) {
	let framed = lint.WriteFrame(payload)
	let mut refused = 0
	let _ = lint.ReadFrame(framed + extra + "x") catch {
	case lint.FrameMalformed:
		refused = refused + 1
		lint.Frame{Version: 0, Payload: ""}
	}
	let _ = lint.ReadFrame(strings.Slice(framed, 0, len(framed)-1)) catch {
	case lint.FrameMalformed:
		refused = refused + 1
		lint.Frame{Version: 0, Payload: ""}
	}
	assert refused == 2
}

test LintFamilyRoundTrips(x lint.Family) {
	let back = lint.ParseFamily(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintTypeKindRoundTrips(x lint.TypeKind) {
	let back = lint.ParseTypeKind(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintCallableKindRoundTrips(x lint.CallableKind) {
	let back = lint.ParseCallableKind(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintCallKindRoundTrips(x lint.CallKind) {
	let back = lint.ParseCallKind(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintImportKindRoundTrips(x lint.ImportKind) {
	let back = lint.ParseImportKind(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintSeverityRoundTrips(x lint.Severity) {
	let back = lint.ParseSeverity(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintOptionTypeRoundTrips(x lint.OptionType) {
	let back = lint.ParseOptionType(x.Wire()) catch {
	case lint.UnknownKind:
		assert false
		x
	}
	assert back == x
}

test LintAnswerAdmitsAndChecks(offset int, length int, sameVersion bool) {
	let o = offset % 20
	let l = 16 - o + length%3
	let span = lint.Span{File: "a.ergo", Offset: o, Length: l, Line: 1, Column: 1}
	let rule = lint.Rule{Name: "probe", Version: "1", Description: "d", DefaultSeverity: lint.Warning, Requires: []lint.Family{}, Options: []lint.OptionSpec{}, Check: func(s lint.Snapshot, options json.Value) []lint.Finding fails lint.RuleFailed {
		[]lint.Finding{lint.Finding{Message: "m", Span: span, Related: []lint.Related{}, Suggestions: []lint.Suggestion{}}}
	}}
	let p = lint.Pack{Namespace: "ns", Version: "0", Description: "", FactVersions: []int{lint.SchemaVersion}, Rules: []lint.Rule{rule}, Presets: []lint.Preset{}}
	let s = lint.Snapshot{Schema: lint.Schema{Name: lint.SchemaName, Version: lint.SchemaVersion}, Semantic: lint.Semantic{Revision: "r", Target: "go", Producer: "", ReuseScope: ""}, Sources: []lint.Source{lint.Source{File: "a.ergo", URI: "", Bytes: 16}}, Checked: true, Families: []lint.Family{lint.CallsFamily}, Unavailable: []lint.UnavailableFamily{}, Types: []lint.TypeDecl{}, Contexts: []lint.Context{}, Callables: []lint.Callable{}, Calls: []lint.Call{}, Withs: []lint.With{}, Bindings: []lint.Binding{}, Imports: []lint.Import{}}
	let version = if sameVersion { "1" } else { "2" }
	let text = "{\"id\":\"i\",\"pack\":{\"namespace\":\"ns\",\"version\":\"0\",\"identity\":\"x\"},\"rules\":[{\"name\":\"probe\",\"version\":\"" + version + "\",\"options\":{}}],\"snapshot\":" + lint.EncodeSnapshot(s) + "}"
	let mut answered = true
	let answer = lint.Answer(p, lint.WriteFrame(text)) catch {
	case _:
		answered = false
		""
	}
	assert answered
	let frame = lint.ReadFrame(answer) catch {
	case lint.FrameMalformed:
		assert false
		lint.Frame{Version: 0, Payload: ""}
	}
	let inside = o >= 0 && l >= 0 && o <= 16 && l <= 16-o
	let want = if !sameVersion {
		"\"status\":\"failed\",\"reason\":\"manifest declares rule version 2; the pack implements 1\""
	} else if inside {
		"\"status\":\"completed\",\"findings\":[{\"message\":\"m\""
	} else {
		"\"status\":\"failed\",\"reason\":\"finding \\\"m\\\": span {File:a.ergo Offset:"
	}
	assert strings.Contains(frame.Payload, want)
}
`

// stdLintLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdLintLawStatements = map[string]string{
	"lint SnapshotRoundTrips":     `SnapshotRoundTrips(s Snapshot) { let back = DecodeSnapshot(EncodeSnapshot(s)) catch { case SnapshotFailed: return false } back == s }`,
	"lint FrameRoundTrips":        `FrameRoundTrips(payload string) { let f = ReadFrame(WriteFrame(payload)) catch { case FrameMalformed: return false } f == Frame{Version: ProtocolVersion, Payload: payload} }`,
	"lint FrameIsExact":           `FrameIsExact(payload string, extra string) { let framed = WriteFrame(payload) let _ = ReadFrame(framed + extra + "x") catch { case FrameMalformed: let _ = ReadFrame(strings.Slice(framed, 0, len(framed)-1)) catch { case FrameMalformed: return true } return false } false }`,
	"lint FamilyRoundTrips":       `FamilyRoundTrips(x Family) { try ParseFamily(x.Wire()) == x }`,
	"lint TypeKindRoundTrips":     `TypeKindRoundTrips(x TypeKind) { try ParseTypeKind(x.Wire()) == x }`,
	"lint CallableKindRoundTrips": `CallableKindRoundTrips(x CallableKind) { try ParseCallableKind(x.Wire()) == x }`,
	"lint CallKindRoundTrips":     `CallKindRoundTrips(x CallKind) { try ParseCallKind(x.Wire()) == x }`,
	"lint ImportKindRoundTrips":   `ImportKindRoundTrips(x ImportKind) { try ParseImportKind(x.Wire()) == x }`,
	"lint SeverityRoundTrips":     `SeverityRoundTrips(x Severity) { try ParseSeverity(x.Wire()) == x }`,
	"lint OptionTypeRoundTrips":   `OptionTypeRoundTrips(x OptionType) { try ParseOptionType(x.Wire()) == x }`,
	"lint AnswerAdmitsAndChecks":  `AnswerAdmitsAndChecks(offset int, length int, sameVersion bool) { let o = offset % 20 let l = 16 - o + length%3 let span = Span{File: "a.ergo", Offset: o, Length: l, Line: 1, Column: 1} let rule = Rule{Name: "probe", Version: "1", Description: "d", DefaultSeverity: Warning, Requires: []Family{}, Options: []OptionSpec{}, Check: func(s Snapshot, options json.Value) []Finding fails RuleFailed { []Finding{Finding{Message: "m", Span: span, Related: []Related{}, Suggestions: []Suggestion{}}} }} let p = Pack{Namespace: "ns", Version: "0", Description: "", FactVersions: []int{SchemaVersion}, Rules: []Rule{rule}, Presets: []Preset{}} let s = Snapshot{Schema: Schema{Name: SchemaName, Version: SchemaVersion}, Semantic: Semantic{Revision: "r", Target: "go", Producer: "", ReuseScope: ""}, Sources: []Source{Source{File: "a.ergo", URI: "", Bytes: 16}}, Checked: true, Families: []Family{CallsFamily}, Unavailable: []UnavailableFamily{}, Types: []TypeDecl{}, Contexts: []Context{}, Callables: []Callable{}, Calls: []Call{}, Withs: []With{}, Bindings: []Binding{}, Imports: []Import{}} let version = if sameVersion { "1" } else { "2" } let text = "{\"id\":\"i\",\"pack\":{\"namespace\":\"ns\",\"version\":\"0\",\"identity\":\"x\"},\"rules\":[{\"name\":\"probe\",\"version\":\"" + version + "\",\"options\":{}}],\"snapshot\":" + EncodeSnapshot(s) + "}" let answer = Answer(p, WriteFrame(text)) catch { case _: return false } let frame = ReadFrame(answer) catch { case FrameMalformed: return false } let inside = o >= 0 && l >= 0 && o <= 16 && l <= 16-o let want = if !sameVersion { "\"status\":\"failed\",\"reason\":\"manifest declares rule version 2; the pack implements 1\"" } else if inside { "\"status\":\"completed\",\"findings\":[{\"message\":\"m\"" } else { "\"status\":\"failed\",\"reason\":\"finding \\\"m\\\": span {File:a.ergo Offset:" } strings.Contains(frame.Payload, want) }`,
}

// TestStdLintLawsRun runs the lint probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestStdLintLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdlint", stdLintLawsProbe, stdLintLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
