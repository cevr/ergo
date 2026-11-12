package lower

import "testing"

// TestCatchWiredRowLowers is the smallest repro of LINT4 bug "wired row
// is not a row value": in a generic function over a wire type parameter
// T, a catch of json.Decode[T] with a record arm (case json.Malformed)
// and `case _` stopped the lowering with S109 "the anonymous union
// json.Malformed | WireWrongKind | wired[T] is not a row value". The
// caught row holds wired[T], the decode failures of T's argument, and
// isErrorValue did not take it for error types as it takes a row
// parameter's member. The lowered Go type-checks in process (typecheckGo).
//
// Mutant lint4-lower-wired-not-error (isErrorValue refuses wired[T]).
func TestCatchWiredRowLowers(t *testing.T) {
	const src = `import json

type Bad { Reason string }

func read[T wire](text string) T fails Bad {
	json.Decode(json.Codec[T]{MaxBytes: 0, MaxDepth: 0}, text) catch {
	case json.Malformed as m:
		return Bad{Reason: m.Reason}
	case _:
		return Bad{Reason: "form"}
	}
}

type P { N int }

func main() {
	let _ = read[P]("{}") catch {
	case Bad:
		P{N: 0}
	}
}
`
	out := lowerSource("catchwired", src, true)
	if out.err != nil || out.panicked != nil || len(out.diags) > 0 || out.refused != nil {
		t.Fatalf("lowering: err %v, panic %v, diagnostics %v, refusals %v", out.err, out.panicked, out.diags, out.refused)
	}
	typecheckGo(t, []*lowered{out})
}
