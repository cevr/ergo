package lower

import (
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// stdLintLawsProbe restates the laws of std lint's laws.ergo as Ergo
// property tests over the package's exported API.
const stdLintLawsProbe = `import lint
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

test LintNamesReadBack(t lint.OptionType, s lint.Severity) {
	let parsedType = lint.ParseOptionType(t.Wire()) catch {
	case lint.UnknownKind:
		assert false
		t
	}
	let parsedSeverity = lint.ParseSeverity(s.Wire()) catch {
	case lint.UnknownKind:
		assert false
		s
	}
	assert parsedType == t && parsedSeverity == s
}
`

// stdLintLawStatements is the statement of each law the probe restates,
// by ledger key; lawledger.Run holds it equal to the ledger.
var stdLintLawStatements = map[string]string{
	"lint SnapshotRoundTrips": `SnapshotRoundTrips(s Snapshot) { let back = DecodeSnapshot(EncodeSnapshot(s)) catch { case SnapshotFailed: return false } back == s }`,
	"lint FrameRoundTrips":    `FrameRoundTrips(payload string) { let f = ReadFrame(WriteFrame(payload)) catch { case FrameMalformed: return false } f == Frame{Version: ProtocolVersion, Payload: payload} }`,
	"lint FrameIsExact":       `FrameIsExact(payload string, extra string) { let framed = WriteFrame(payload) let _ = ReadFrame(framed + extra + "x") catch { case FrameMalformed: let _ = ReadFrame(strings.Slice(framed, 0, len(framed)-1)) catch { case FrameMalformed: return true } return false } false }`,
	"lint NamesReadBack":      `NamesReadBack(t OptionType, s Severity) { let parsedType = ParseOptionType(t.Wire()) catch { case UnknownKind: return false } let parsedSeverity = ParseSeverity(s.Wire()) catch { case UnknownKind: return false } parsedType == t && parsedSeverity == s }`,
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
