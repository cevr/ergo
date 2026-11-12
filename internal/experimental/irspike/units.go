package irspike

import "strings"

// UnitOf is the coverage unit that owns a census reason (plan-tier §2), or
// "" when no unit owns it yet: TestIRCoverage fails on such a reason.
func UnitOf(reason string) string {
	for _, r := range reasonUnits {
		if r.prefix == reason || strings.HasSuffix(r.prefix, "*") && strings.HasPrefix(reason, strings.TrimSuffix(r.prefix, "*")) {
			return r.unit
		}
	}
	return ""
}

// reasonUnits maps each reason (exact, or a prefix ending in *) to its unit.
var reasonUnits = []struct{ prefix, unit string }{
	// G1: scalars and small forms.
	{"basic type *", "G1"},
	{"unnamed parameter", "G1"},
	{"name that is not a local", "G1"},
	{"expression *syntax.InterpolatedString", "G1"},
	{"interpolated string form", "G1"},
	// Statement kinds of other units first: the first match wins.
	{"expression statement *syntax.TryExpr", "G2"},
	{"expression statement *syntax.CatchExpr", "G2"},
	{"expression statement *syntax.SwitchStmt", "G3"},
	{"expression statement *syntax.TypeSwitchStmt", "G3"},
	{"expression statement *syntax.FuncLit", "G6"},
	{"expression statement *", "G1"},
	{"expression *syntax.IfStmt", "G1"},
	{"break or continue outside a loop", "G1"},
	{"result statement *syntax.RangeStmt", "G1"},
	{"assignment form several", "G1"},
	{"type *check.Tuple", "G1"},
	{"expression *syntax.TupleExpr", "G1"},
	{"let without a variable", "G1"},
	{"let pattern*", "G1"},
	{"expression *syntax.SliceExpr", "G1"},
	{"type *check.Array", "G1"},
	{"index of a non-slice", "G1"},
	{"index form", "G1"},
	{"len of a non-slice", "G1"},
	{"len form", "G1"},
	{"operator *", "G1"},
	{"unary *", "G1"},
	{"expression *syntax.StarExpr", "G1"},
	{"selector on a non-record", "G1"},
	{"selector that is not a field", "G1"},
	{"record update or elided literal type", "G1"},
	{"unkeyed record literal", "G1"},
	{"record literal without every field", "G1"},
	{"keyed slice literal", "G1"},
	{"spread", "G1"},
	{"assignment form *", "G1"},
	{"assignment to a place", "G1"},
	{"assignment to a non-local", "G1"},
	{"statement *syntax.BranchStmt", "G1"},
	{"for without a condition", "G1"},
	{"result statement *syntax.ForStmt", "G1"},
	{"result statement *syntax.DeclStmt", "G1"},
	{"result statement *syntax.AssignStmt", "G1"},
	{"if without else in result position", "G1"},
	{"empty body with a result", "G1"},
	{"bare return", "G1"},
	{"constant kind *", "G1"},
	{"big integer constant", "G1"},
	{"local declared twice", "G1"},
	{"for-in pattern", "G1"},
	{"for-in binding without a variable", "G1"},
	// G2: the failure ABI, frames and operation calls.
	{"fallible function", "G2"},
	{"expression *syntax.CatchExpr", "G2"},
	{"expression *syntax.TryExpr", "G2"},
	{"loop in an impure frame", "G2"},
	{"call of an impure, fallible or special function", "G2"},
	{"operation call form", "G2"},
	{"operation call spelling", "G2"},
	{"capability without a named type", "G2"},
	{"call in an operation's argument", "G2"},
	{"operation call in a range", "G2"},
	{"operation call in a condition", "G2"},
	{"operation call inside an expression", "G2"},
	{"body that needs a frame", "G2"},
	{"owned local", "G2"},
	{"owned temporary", "G2"},
	{"statement that releases owned values", "G2"},
	{"op-assignment of a value with statements", "G2"},
	{"untried fallible call", "G2"},
	{"fallible call with an empty row at its site", "G2"},
	{"call of an impure function from a pure one", "G2"},
	{"try without a value", "G2"},
	{"try of a non-call", "G2"},
	{"catch without its Matches entry", "G2"},
	{"catch of a non-call", "G2"},
	{"catch arm without a pattern", "G2"},
	{"catch arm form", "G2"},
	{"continue in a polling loop", "G2"},
	{"failure in a branch tail of a body with no value", "G2"},
	{"operation call that enters a release", "G2"}, // a checked Close: frames, IRG2b
	{"call of a pointer-receiver method", "G4"},
	{"call of an extern function", "G4"},
	{"call that passes owned values", "G4"},
	{"call that passes witnesses", "G4"},
	{"call with a call site", "G4"},
	{"call that captures owners", "G4"},
	{"catch with patterns", "G3b"},        // record patterns, case err, case _: the match tree's
	{"catch value of another type", "G3"}, // a flat union the arms join to
	// G3: unions, match, machines; the lowering's representation decides.
	{"named type that is not a record", "G3"},
	{"record without a struct", "G3"},
	{"result statement *syntax.TypeSwitchStmt", "G3"},
	{"statement *syntax.TypeSwitchStmt", "G3"},
	{"expression *syntax.TypeSwitchStmt", "G3"},
	{"result statement *syntax.SwitchStmt", "G3"},
	{"statement *syntax.SwitchStmt", "G3"},
	{"expression *syntax.SwitchStmt", "G3"},
	{"implicit conversion", "G3b"}, // of a fallible call's result (ret)
	{"constant of type *", "G3"},
	// G3b (IRG3's follow-up): the match tree, scalar-form locals, machines.
	// A loop-carried union local in tag-and-slots form (scalar.go): a
	// union representation decision.
	{"scalar-form local", "G3b"},
	{"match with patterns", "G3b"},  // the decision tree (matchtree.go)
	{"break inside a match", "G3b"}, // Go's break leaves the switch: a labelled loop
	{"interpolated value of type *", "G3b"},
	{"machine conversion", "G3b"},
	{"machine value", "G3b"},
	{"row conversion", "G3b"},
	{"type defined over a non-basic type", "G3b"},
	{"value of a type defined over a flat union", "G3b"},
	{"value of another type", "G3b"},
	{"match on a value that is no union", "G3b"},
	{"match on a value of type *", "G3b"},
	{"match subject that releases owned values", "G2"},
	{"match on Get", "G4"},         // a Map read: the comma-ok form (getmatch.go)
	{"operation-type value", "G4"}, // a value of an operation type: an interface of functions
	{"operations conversion", "G4"},
	{"result statement *syntax.BranchStmt", "G1"},
	// G4: generics, packages, tasks, references, function values.
	{"generic or declared-elsewhere type", "G4"},
	{"call of a function outside the package", "G4"},
	{"generic or task call", "G4"},
	{"deep type", "G4"},
	{"type *check.Map", "G4"},
	{"type *check.TypeParam", "G4"},
	{"type *check.Signature", "G4"},
	{"type *check.Pointer", "G4"},
	{"pointer receiver", "G4"},
	{"for-in over a non-slice", "G4"},
	{"call kind 1", "G4"}, // CallOp: an operation-type value's operation
	{"call kind 3", "G4"}, // CallFuncValue
	{"call kind 4", "G4"}, // CallConversion
	{"Go type spelling *", "G4"},
	{"witness parameter", "G4"},
	{"generic body", "G4"},
	{"owned parameters", "G4"},
	{"known-callback variant", "G4"},
	{"copy for a known record", "G4"},
	{"call without a value", "G4"},
	{"call without a Calls entry", "G4"},
	{"value copy", "G4"}, // the value rule's copy (values.go)
	// G5: std bodies.
	{"library body", "G5"},
	{"intrinsic body", "G5"},
	{"std body not reachable alone", "G5"},
	// G6: the rest (closures, codecs, test and law bodies).
	{"expression *syntax.FuncLit", "G6"},
	{"trailing block", "G6"}, // with(…) { }: a context and its frame
	{"context value", "G6"},
	{"call kind 10", "G6"}, // CallEncode
	{"call kind 11", "G6"}, // CallDecode
	{"call kind 12", "G6"}, // CallEmbed
	{"call kind 8", "G6"},  // CallLawOp
	{"call kind 9", "G6"},  // CallGoError
}
