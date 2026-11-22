package lower

import "testing"

// TestBareRecordThroughAlias is the smallest repro of S109-ALIAS (AF1's
// pipeline view, `pipelinerpc.BoardRequest` with `type BoardRequest =
// pipelineapi.BoardRequest`): a record with no fields written bare through
// a type alias of another package lowered to an internal compiler error,
// "the selector wire.Board is no function or let of the imported package",
// and the IR fell back on it. Both lowerings now give the aliased record's
// zero value, and the Go type-checks (typecheckGo).
//
// Mutants it kills (testdata/mutants.json): s109-alias-text,
// s109-alias-ir.
func TestBareRecordThroughAlias(t *testing.T) {
	hub := checkPackage(t, "hub", []pkgSource{{"/ergo-test/hub/hub.ergo", `type Board {}

type Move { N int }

type Request = Board | Move
`}}, nil)
	wire := checkPackage(t, "wire", []pkgSource{{"/ergo-test/wire/wire.ergo", `import hub

type Board = hub.Board

type Move = hub.Move
`}}, pkgImporter{"hub": hub.Package})
	main := checkPackage(t, "main", []pkgSource{{"/ergo-test/alias/main.ergo", `import hub
import wire

func pick(n int) hub.Request {
	if n > 0 {
		return wire.Move{N: n}
	}
	wire.Board
}

func main() needs Console {
	match pick(0) {
	case hub.Board:
		Console.Println("board")
	case hub.Move as m:
		Console.Println("move \(m.N)")
	}
}
`}}, pkgImporter{"hub": hub.Package, "wire": wire.Package})
	main.Imports = []Unit{hub, wire}
	o := mustLowerUnit(t, "alias_bare", main, false)
	files, rep, err := LowerIR(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) > 0 {
		t.Fatalf("IR fallback: %v", rep.Fallback)
	}
	typecheckGo(t, []*lowered{o, {name: "alias_bare_ir", src: o.src, files: files}})
}
