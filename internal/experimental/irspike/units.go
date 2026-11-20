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
	{"let of a default *", "G1"}, // F7 letpat (ir_letpat.go)
	{"expression *syntax.SliceExpr", "G1"},
	{"type *check.Array", "G1"},
	{"literal of an array whose default holds a Map", "G1"}, // INDEX-NONSLICE: the emitter's default, built in a loop
	{"index of a non-slice", "G1"},
	{"index form", "G1"},
	{"len of a non-slice", "G1"},
	{"len form", "G1"},
	{"operator *", "G1"},
	{"unary *", "G1"},
	{"expression *syntax.StarExpr", "G1"},
	{"selector on a non-record", "G1"},
	// F7 select (ir_select.go): the qualified names it leaves.
	// F7-cs-fnname (ir_fnname.go): the function values funcValue writes
	// another way than a closure that calls the function.
	{"function value without a function type", "G6"},
	{"Go function as a value", "G6"},
	{"user extern as a value", "G6"},
	{"method as a value", "G6"},
	{"function that takes witnesses as a value", "G4"},
	{"function value of another arity", "G6"},
	{"std extern as a value", "G6"},
	{"extern as a value", "G6"},
	{"generic function as a value", "G4"},
	{"function value whose result differs from its function's", "G6"},
	{"Go package variable", "G4"},
	{"package let without a Go name", "G1"},
	{"package type name as a value", "G1"},
	{"selector that is not a field", "G1"},
	{"record update or elided literal type", "G1"},
	// F7 update (ir_update.go): the updates it leaves.
	{"elided literal type", "G1"},
	{"record update *", "G1"},
	{"record literal without every field", "G1"},
	{"keyed slice literal", "G1"},
	{"spread", "G1"},
	{"assignment form *", "G1"},
	{"assignment to a non-local", "G1"},
	// F2 lowers places (stores, &p, *p, pointer receivers); these remain.
	{"store to a place of the IR kind *", "F2"}, // PORT-TEMP's fixPlace: a place form it does not know
	{"place that is not addressable", "F2"},     // a Map entry: Maps are G4's
	{"place of the IR kind *", "F2"},
	{"place with a conversion", "F2"},
	{"place of a handed-off local", "G2"},            // a Close through its address
	{"place of a taken field", "G2"},                 // takeField: an owned field
	{"store of a call that updates its place", "F3"}, // mapStore: an append or Update in place
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
	{"loop in an impure frame", "G2"},
	{"call of an impure, fallible or special function", "G2"},
	{"operation call form", "G2"},
	{"operation call spelling", "G2"},
	{"capability without a named type", "G2"},
	{"call in an operation's argument", "G2"},
	{"operation call inside an expression", "G2"},
	{"body that needs a frame", "G2"},
	{"owned local", "G2"},
	{"owned temporary", "G2"},
	{"statement that releases owned values", "G2"},
	{"untried fallible call", "G2"},
	{"fallible call with an empty row at its site", "G2"},
	{"call of an impure function from a pure one", "G2"},
	{"try without a value", "G2"},
	{"catch without its Matches entry", "G2"},
	{"catch of a non-call", "G2"},
	{"catch arm without a pattern", "G2"},
	{"catch arm form", "G2"},
	{"operation call that enters a release", "G2"}, // a checked Close: frames, IRG2b
	{"checked Close (closeBody masks it)", "G2"},   // the Close body itself, inside its mask
	{"call of an extern function", "G4"},
	{"call that passes owned values", "G4"},
	{"call that passes witnesses", "G4"}, // a method with witnesses (ir_witness.go)
	{"call that passes the caller's witness", "G4"},
	{"witness without its reserved name", "G4"},
	{"call with a call site", "G4"},
	{"call that captures owners", "G4"},
	{"catch value of another type", "G3"}, // a flat union the arms join to
	// G3: unions, match, machines; the lowering's representation decides.
	{"named type that is not a record", "G3"},
	{"record without a struct", "G3"},
	// A match or an if inside an operand that is not a call's or a
	// binary operator's at the value's top (IRG3b takes those, in operand
	// order): deeper, hoisting it would move it before the operands to its
	// left.
	{"expression *syntax.TypeSwitchStmt", "G3b"},
	{"expression *syntax.SwitchStmt", "G3b"},
	{"implicit conversion", "G3b"}, // a conversion kind the builder does not know
	{"constant of type *", "G3"},
	// G3b (IRG3's follow-up): the match tree, scalar-form locals.
	// A loop-carried union local in tag-and-slots form (scalar.go): a
	// union representation decision; a read deep in an expression gathers
	// with statements.
	{"scalar-form local of a type parameter", "G4"},
	{"scalar-form local *", "G3b"},
	{"op-assignment of a scalar-form local", "G3b"},
	{"match with patterns", "G3b"}, // the flat lowering of a tree (flatPatternMatch)
	{"match tree *", "G3b"},
	{"match binding of another type", "G3b"},
	{"shared arm binding that its first leaf does not make", "G3b"},
	{"record pattern of an unnamed type", "G3b"},
	{"interpolated value of type *", "G3b"},
	{"interpolated named duration", "G3b"},
	{"machine *", "G3b"}, // ir_machine.go's refusals (a literal with fields, a second Machine type)
	{"row conversion", "G3b"},
	{"type defined over a non-basic type", "G3b"},
	{"literal of a defined *", "G3b"},            // Point{…} with elements, of `type Point [8]int32` (GP-DEFINED)
	{"record field of a converted value", "G3b"}, // B-CONVFIELD (ir_field.go): a difference neither a conversion nor a row explains
	{"value of a type defined over a flat union", "G3b"},
	{"value of another type", "G3b"},
	{"match on a value that is no union", "G3b"},
	{"match on a value of type *", "G3b"},
	{"match subject that releases owned values", "G2"},
	{"match on Get", "G4"},                // a Map read: the comma-ok form (getmatch.go)
	{"match on Get with patterns", "G3b"}, // the emitter's type switch on the Option (ir_get.go)
	{"call of Get with *", "G3b"},
	{"Get of a value that is no Map", "G3b"},
	{"operation-type value", "G4"}, // a value of an operation type: an interface of functions
	{"operations conversion", "G4"},
	{"result statement *syntax.BranchStmt", "G1"},
	// G4: generics, packages, tasks, references, function values.
	{"generic or declared-elsewhere type", "G4"},
	{"generic or task call", "G4"}, // a generic std body
	{"task call", "G4"},
	{"generic call kind *", "G4"},
	{"generic call without its type arguments", "G4"},
	{"generic call with a row type argument of the caller", "G4"},
	{"Option's pointer niche", "G4"},
	{"append form", "G4"},
	{"append to a defined slice type", "G4"},
	// slices.AppendAll, Go's append(xs, ys...): ir_builtin.go falls back to
	// the text body (I22); the bulk append lowers with NB-ROLE (NO-BUILTINS
	// findings §7).
	{"append of a slice's elements", "NB-ROLE"},
	{"Map write form", "G4"},
	{"Map write to a defined Map type", "G4"},
	// IRG4 calls other packages' functions and std externs with a plain
	// runtime/v1 body; these call forms remain.
	{"call without a function", "G4"},
	{"call of a Go builtin", "G4"},
	{"call of a user extern", "G4"},
	{"generic Go package type", "G4"},                       // FORK-GOCALL: a Go generic type's instance
	{"Go function passed to a Go package function", "G4"},   // FORK-GOCALL: goDirect
	{"Go callback that fails or has several results", "G4"}, // FORK-GOCALL: GoCallbackError's tunnel
	{"Go callback of another shape", "G4"},                  // FORK-GOCALL: goCallbackIn's copies
	{"call of a Go package function", "G4"},                 // import go (gostd); IRG6 r2 gates it, the census crashed on it
	// FORK-GOLIB's remaining Go call forms (ir_golib.go).
	{"Go call whose site drops its failure", "G4"},
	{"Go context call from a pure function", "G4"},
	{"Go call with a result of another type", "G4"},
	{"fallible Go call with several results", "G4"},
	{"Go method call on a receiver of another type", "G4"},
	{"Go method call that takes its receiver's address", "G4"},
	{"Go callback from a pure function", "G4"},
	{"go.Error literal form", "G4"},
	{"call of a std intrinsic", "G4"},
	{"dot call of a std function", "G4"},
	{"std call *", "G4"}, // hidden arguments, adapted results, converted operands
	// I-FLIP's integration refusals: a value Go converts implicitly, and a
	// leaf's generic function named by a generic body's type parameters.
	{"record field of a converted value", "G4"},
	{"runtime leaf's generic function in a generic body", "G4"},
	{"type *check.Map", "G4"},
	{"type *check.Union", "G4"}, // an anonymous union as a type argument (IRG4b instances)
	// IRG4b r2: operation-type values and their calls, std operations.
	{"operation call from a pure function", "G4"},
	{"owned site *", "G4"}, // the Ref's owned site's refusals (ir_refown.go, IR-OWN)
	{"fallible owned site", "G4"},
	{"owned operation whose form *", "G4"},
	{"owning *", "G4"}, // an owned Ref site's in-place form and its other shapes (ir_ownlit.go)
	{"operation call on a *", "G4"},
	{"std operation without its runtime method", "G4"},
	{"std Close", "G4"},
	{"call of a pointer-receiver method on a value", "G1"}, // &x of a place
	{"for-in over a Range *", "G4"},
	{"conversion *", "G4"},
	{"type argument with no value", "G4"}, // a task's T bound by a block with no value
	{"type *check.TypeParam", "G4"},
	{"for-in over a non-slice", "G4"},
	{"call kind 1", "G4"}, // CallOp: an operation-type value's operation
	{"call kind 4", "G4"}, // CallConversion
	{"Go type spelling *", "G4"},
	{"witness parameter", "G4"},
	{"generic body", "G4"},
	// F1's admit of a type parameter the body does not declare: the
	// merged head's newer std (ui's TestHost functions, machines.Attempt)
	// reaches it through generic instances whose declared types name
	// their own type parameters (F7 FORK merge of F2).
	{"type parameter outside its declaration", "G4"},
	// I-FLIP's f2e93090e: a runtime leaf generic in the body's own type
	// parameters (ir_pkg.go), F1's generic bodies.
	{"runtime leaf's generic function in a generic body", "G4"},
	{"owned parameters", "G4"},
	// LY-FIX: a monomorphic, provider or witness copy keeps its emitter
	// body. (Its Go row gate went with ROWSLOT-IR: such a function and
	// its calls take IR bodies.)
	{"copy for an instance", "G4"},
	{"variant with no result", "G4"}, // drop.go's variant of a function whose result a call discards
	{"call without a value", "G4"},
	{"call without a Calls entry", "G4"},
	{"value copy of a *", "G4"}, // a value rule copy at a defined slice or Map type (ir_copy.go)
	// G5: std bodies.
	// A library body takes an IR body as a sourced one does (IRG5).
	// An extern's body is the lowerer's Go text (Append, Get, Put,
	// Delete, Sleep): Go by plan-tier §1.4 (hand-written Go compiles as
	// Go), not an Ergo body the IR expresses (IRG5).
	{"intrinsic body", "Go"},
	{"std body not reachable alone", "G5"},
	// G6: the rest (closures, codecs, test and law bodies).
	{"trailing block", "G6"}, // a call's trailing block that is not a with's
	{"context value", "G6"},
	// IRG6 lowers a with of named, inline and local contexts, and the
	// operations conversion its providers take (IRG4 left both); the
	// withs and conversions below stay the emitter's.
	{"with *", "G6"},
	{"context entry *", "G6"},
	{"operations conversion *", "G6"},
	{"generic operations conversion", "G6"},
	// IRG6 r2 lowers function values (literals, trailing blocks of program
	// calls, function types, calls of function values; the last two were
	// G4's). These stay the emitter's.
	{"function literal *", "G6"},
	{"function type with several results", "G6"},
	{"call of a function value *", "G6"},
	{"call of a value of a named function type", "G6"},
	{"string constant that is not UTF-8", "G6"}, // the IR file is JSON (IRG6 r3)
	// IRG6 r4 lowers law bodies; these stay the emitter's.
	{"law of a generic operation type", "G6"},
	{"law result of an untried fallible call", "G6"},
	// The codec built-ins take the IR (FLIP OPC-codec), but for these:
	// PORT-WIRE-READERS step 0: the codec functions that keep their text
	// body, counted as census bodies (codecText). A decoder by its form.
	{"codec reader: *", "PORT-WIRE-READERS"},
	{"codec writer on the reference Writer", "PORT-WIRE-REF"},
	{"codec writer on a direct writer that is no cell", "PORT-WIRE-REF"},
	{"codec of a type that holds type parameters", "G4"}, // codec witnesses, with witness parameters
	{"codec *", "G6"},
	{"encode on a direct writer", "G6"},
	// JVD-DIRECT: the IR declines a direct reader's decode as it declines the direct writer's encode.
	{"decode on a direct reader", "G6"},
	{"call kind 12", "G6"}, // CallEmbed
	{"call kind 13", "G6"}, // CallPattern: a refinement's matches(it, `…`)
	{"call kind 9", "G6"},  // CallGoError
	// IRG6 r2's function values, ported to flip-f1 (flip-fnval): literals,
	// trailing blocks of program calls, function types, calls of function
	// values. These stay the emitter's.
	{"function literal *", "G6"},
	{"function type with several results", "G6"},
	{"call of a function value *", "G6"},
	{"call of a value of a named function type", "G6"},
}
