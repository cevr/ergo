package lower

import "testing"

// TestBareUnionDecodeSwitchesOnFlatWireKind is the smallest repro of
// batch-22 Q9: since flat-union-std, the prelude's WireKind lowers as a
// flat union, and the decode of a bare union (WireBare) still switched on
// Reader.Peek's result as a Go interface, so the emitted Go did not
// type-check ("r.Peek(env.task) (value of struct type WireKind) is not an
// interface"; std json's Value, scripts/stdlaws). The decode now switches
// on WireKind's tag. The lowered Go type-checks in process (typecheckGo).
//
// Mutant q9-peek-type-switch (the decode takes WireKind for an interface).
func TestBareUnionDecodeSwitchesOnFlatWireKind(t *testing.T) {
	const src = `import json

type NilValue {}
type Flag { Data bool }
type Word { Data string }
type Tree = NilValue | Flag | Word
func WireForm(x Tree) WireBare { WireBare }

func main() needs Console {
	let t = json.Decode(json.Codec[Tree]{MaxBytes: 0, MaxDepth: 0}, "true") catch {
	case _:
		NilValue
	}
	match t {
	case Flag as f:
		Console.Println("\(f.Data)")
	case NilValue:
	case Word:
	}
}
`
	out := lowerSource("barewirekind", src, true)
	if out.err != nil || out.panicked != nil || len(out.diags) > 0 || out.refused != nil {
		t.Fatalf("lowering: err %v, panic %v, diagnostics %v, refusals %v", out.err, out.panicked, out.diags, out.refused)
	}
	typecheckGo(t, []*lowered{out})
}
