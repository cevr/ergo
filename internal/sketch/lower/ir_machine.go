package lower

// IR builder: machine values (FLIP F7 wave 2, "machine value").
//
// A machine declaration's Go form is its plan, a zero-size struct
// (machinePlan, machine.go), so a value of it is the literal X{} and the
// type needs no admission beyond its name. Where a machine value converts
// to the prelude Machine or InputMachine (ConvMachine), the IR calls one
// generated function per machine, ergoMachineX(v X) Machine[…], whose body
// is the emitter's conversion (expr.go convert): the plan's Initial or the
// start function, the step's value, the After and the Work. None of those
// closures captures the caller's frame (each takes its environment as a
// parameter, callerEnv), so one package function serves every site.
//
// Debt (as FORK's release helper, spec "Frames on IR"): the function's
// body is Go text from the emitter. Follow-up MACHINE-IR writes it as a
// named IR function once the IR has function literals.
//
// Prior art: Gleam and ReScript compile a record of functions to a host
// object built once; Elixir's gen_statem callbacks are module functions,
// not closures over the caller.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// machineValueKey is the package-scope key of the machine tn's generated
// value function.
func machineValueKey(tn *check.TypeName) pkgKey { return pkgKey{"machine value", tn} }

// reserveMachineValue names, in the naming pass, the generated value
// function of the machine tn (machineValueFunc). Reserving one the
// lowering does not emit only renames a local.
func (l *lowerer) reserveMachineValue(tn *check.TypeName) {
	l.pkgName(machineValueKey(tn), "ergoMachine"+tn.Name)
}

// admitMachine admits a machine declaration's type: its plan, a Go struct
// with no fields and no type parameters (the grammar gives a machine none).
func (b *irBuilder) admitMachine(t *check.Named) {
	if len(t.TArgs) > 0 {
		irNo("machine value with type arguments")
	}
}

// funcField reports that a record field's type t is a function type,
// which a record's admission skips: the IR never spells a field's type
// (the decoder reads it from the Go struct), and every place that would
// spell or call a function value (a local of it, a call of a value) admits
// or refuses the function type there. So a record of functions, such as
// the prelude Machine a machine value converts to, moves through an IR
// body as a whole value.
func funcField(t check.Type) bool {
	_, ok := t.(*check.Signature)
	return ok
}

// machineLit is the IR of the machine literal x, X{}: the plan's zero
// value.
func (b *irBuilder) machineLit(x *syntax.CompositeLit, lit *irExpr) *irExpr {
	if len(x.Elts) > 0 {
		irNo("machine literal with fields")
	}
	return lit
}

// machineValue is e, a value of a machine declaration, converted to the
// prelude Machine or InputMachine (ConvMachine): a call of the machine's
// generated value function.
func (b *irBuilder) machineValue(conv check.Conversion, e *irExpr, site syntax.Node) *irExpr {
	n, ok := conv.From.(*check.Named)
	if !ok || b.l.typeDecl(n.Obj, site).kind != kindMachine {
		irNo("machine conversion from a non-machine")
	}
	b.admitMachine(n)
	return &irExpr{K: "call", Pos: e.Pos, Name: b.l.machineValueFunc(conv, n, site), Args: []*irExpr{e}}
}

// machineValueFunc is the name of the machine n's generated value
// function, func(v X) To, declared once in the generated code. A second
// conversion of n to another Machine type is refused: the checker gives a
// machine one Machine type (its step's signature fixes O, F and R).
func (l *lowerer) machineValueFunc(conv check.Conversion, n *check.Named, site syntax.Node) string {
	key := machineValueKey(n.Obj)
	name := l.pkgName(key, "ergoMachine"+n.Obj.Name)
	scope := newNamer(l.names)
	fc := newFuncCtx(l, scope, false)
	fc.site = site
	to := fc.goType(conv.To)
	if l.machines == nil {
		l.machines = map[pkgKey]string{}
	}
	if have, ok := l.machines[key]; ok {
		if have != to {
			irNo("machine value at a second Machine type")
		}
		return name
	}
	l.machines[key] = to
	v := scope.alloc("v")
	fc.depth = 1
	val := fc.convert(conv, v, site)
	var b strings.Builder
	b.WriteString("func " + name + "(" + v + " " + fc.goType(conv.From) + ") " + to + " {\n")
	b.WriteString(fc.out.String())
	b.WriteString("\treturn " + val + "\n}\n\n")
	l.gen.WriteString(b.String())
	return name
}
