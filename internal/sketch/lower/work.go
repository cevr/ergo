package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// workLit lowers a Work literal of the work rule's forms (check/work.go;
// runtime.md, Machines and actors 12) to the std machines.Work record whose
// Start runs the work and makes its signals:
//
//	Work[E]{Start: func(env *Env, send func(*Env, E) (struct{}, error)) (struct{}, error) {
//		t, err := run(env)          // Listen: _, err := listen(env, send)
//		if err != nil {             // a typed failure: OnFailure, or the failure is a signal
//			e, _ := onFailure(env, err.(F))
//			return send(env, e)
//		}
//		e, _ := onSuccess(env, t)   // or e := t: the result is a signal
//		return send(env, e)         // Listen: its end sends nothing
//	}}
//
// An interruption and a defect are Go panics, not errors (rows.md, Catch
// 6), so they pass through Start: the runner sends nothing for the one
// and ends the actor for the other. The maps have no error row, so their
// error is always nil. The function values are bound once, where the
// literal is made.
func (fc *funcCtx) workLit(x *syntax.CompositeLit, lit *check.WorkLit, t check.Type) string {
	fn := lit.Run
	if fn == nil {
		fn = lit.Listen
	}
	values := []syntax.Expr{fn}
	if lit.OnSuccess != nil {
		values = append(values, lit.OnSuccess)
	}
	if lit.OnFailure != nil {
		values = append(values, lit.OnFailure)
	}
	vals := fc.operands(values, true)
	run := fc.bindFunc(vals[0])
	var onSuccess, onFailure string
	i := 1
	if lit.OnSuccess != nil {
		onSuccess = fc.bindFunc(vals[i])
		i++
	}
	if lit.OnFailure != nil {
		onFailure = fc.bindFunc(vals[i])
	}
	signal := fc.goType(lit.Signal)
	start := fc.envClosure(callerEnv, "env", x, func(env string) ([]string, string, string) {
		send := fc.scope.alloc("send")
		ind := strings.Repeat("\t", fc.depth+1)
		in := ind + "\t"
		res, err, e := fc.scope.alloc("r"), fc.scope.alloc("err"), fc.scope.alloc("e")
		var b strings.Builder
		if lit.Run != nil {
			b.WriteString(ind + res + ", " + err + " := " + run + "(" + env + ")\n")
		} else {
			b.WriteString(ind + "_, " + err + " := " + run + "(" + env + ", " + send + ")\n")
		}
		// A typed failure becomes a signal.
		b.WriteString(ind + "if " + err + " != nil {\n")
		switch {
		case onFailure != "":
			failure := err
			if fc.goType(lit.Failure) != "error" {
				failure = err + ".(" + fc.goType(lit.Failure) + ")"
			}
			b.WriteString(in + e + ", _ := " + onFailure + "(" + env + ", " + failure + ")\n")
			b.WriteString(in + "return " + send + "(" + env + ", " + e + ")\n")
		case lit.Failures.Empty():
			// No typed failure: a non-nil err is a defect or an interrupt,
			// which the work passes on. A one-variant signal is its variant
			// (F11), not an interface, so err.(Signal) would not compile
			// (batch-13 integration, corpus c988).
			b.WriteString(in + "return struct{}{}, " + err + "\n")
		default:
			if fu := fc.l.flatUnionOf(lit.Signal); fu != nil {
				// The failure is a variant of the signal, an error value: a
				// flat signal union holds no error value, so the signal
				// keeps its interface (flatunion.go; MU4 x MU8b).
				flatBail(x, fu)
			}
			b.WriteString(in + e + " := " + err + ".(" + signal + ")\n")
			b.WriteString(in + "return " + send + "(" + env + ", " + e + ")\n")
		}
		b.WriteString(ind + "}\n")
		if lit.Run != nil {
			if onSuccess != "" {
				b.WriteString(ind + e + ", _ := " + onSuccess + "(" + env + ", " + res + ")\n")
			} else {
				b.WriteString(ind + "var " + e + " " + signal + " = " + res + "\n")
			}
			b.WriteString(ind + "return " + send + "(" + env + ", " + e + ")\n")
		} else {
			b.WriteString(ind + "return struct{}{}, nil\n")
		}
		return []string{send + " " + fc.goType(lit.Send)}, " (struct{}, error)", b.String()
	})
	return fc.goType(t) + "{Start: " + start + "}"
}
