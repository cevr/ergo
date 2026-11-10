package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// An Ergo value as a Go interface (IG11; docs/spec/interop.md, Go calls
// Ergo). The checker admits a value of a type of the program where a Go
// interface type of import go is expected when the type's functions
// provide the interface's operations (an operations conversion, as for an
// operation type): io.WriteString(w, s) with an Ergo record w that has
// Write. Go calls the interface's methods with Go's signatures, and no
// method of the Ergo type has them (an Ergo function takes an environment
// and returns its error in Ergo's shape), so the value is wrapped in an
// adapter: one Go type per Go interface, whose methods call function
// fields, and at the conversion one Go callback per operation (goCallback,
// IG6), which runs the Ergo function in the environment of the code that
// converts: its task and the capabilities its row takes from there (only
// go.Foreign: a provider of a Go interface needs nothing else, as the
// interface's operations name nothing else). Go's arguments and results
// cross as a callback's do: a go.Error is Go's own error, a slice a copy,
// and a failure Go's error (v1.GoCallbackError). A defect in the method is
// a Go panic through Go's frames, a defect of the code that called Go.
//
// The adapter is the reference lowering: a closure per operation per
// conversion. Hand Go implements the interface on its own type and boxes
// it (one allocation); the closures cost one more each (bench-pending, IG11
// control).

// hostSourced reports a type whose values are Go's own: a type of import
// go, a go.Ref of one or a borrow of one. Its conversion to a Go interface
// is Go's implicit conversion.
func hostSourced(t check.Type) bool {
	if e, ok := check.GoRefElem(t); ok {
		t = e
	}
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*check.Named)
	return ok && n.Obj != nil && n.Obj.Pkg.Host()
}

// goInterface converts val, a value of a type of the program, to the Go
// interface type to: the adapter of to, whose fields are the Go callbacks
// of conv's table functions, each bound to val.
func (fc *funcCtx) goInterface(conv check.Conversion, to *check.Named, val string, site syntax.Node) string {
	it, ok := to.Underlying().(*check.Interface)
	if !ok {
		ice(site, "an operations conversion to the Go type %s, which is not an interface", to)
	}
	l := fc.l
	key := goInterfaceKey(l, to)
	name, made := l.adapters[key]
	if !made {
		name = l.pkgName(pkgKey{"adapter", key}, "goAs"+to.Obj.Name)
		l.adapters[key] = name
		var b strings.Builder
		b.WriteString("// " + name + " implements the Go interface " + to.String() + " by its function fields (IG11).\n")
		b.WriteString("type " + name + " struct {\n")
		var methods strings.Builder
		for _, op := range it.Methods {
			var params, args []string
			for i, p := range op.Sig.Params {
				n := "p" + strconv.Itoa(i)
				params = append(params, n+" "+fc.goCallbackType(p.Type))
				args = append(args, n)
			}
			var results []string
			if len(op.Sig.Results) == 1 {
				results = append(results, fc.goCallbackType(op.Sig.Results[0]))
			}
			if goFails(op.Sig) {
				results = append(results, "error")
			}
			res := ""
			switch len(results) {
			case 1:
				res = " " + results[0]
			case 2:
				res = " (" + strings.Join(results, ", ") + ")"
			}
			field := "f" + op.Name
			b.WriteString("\t" + field + " func(" + strings.Join(params, ", ") + ")" + res + "\n")
			ret := "return "
			if len(results) == 0 {
				ret = ""
			}
			methods.WriteString("func (a " + name + ") " + op.Name + "(" + strings.Join(params, ", ") + ")" + res + " {\n\t" + ret + "a." + field + "(" + strings.Join(args, ", ") + ")\n}\n\n")
		}
		b.WriteString("}\n\n")
		b.WriteString(methods.String())
		l.gen.WriteString(b.String())
	}
	recv := fc.spill(val)
	var fields []string
	for _, op := range it.Methods {
		f := conv.Table[op.Name]
		if f == nil {
			ice(site, "the conversion to the Go interface %s has no function for %s", to, op.Name)
		}
		fv := fc.bindFunc(fc.funcValue(f, f.Sig, site, nil))
		fields = append(fields, "f"+op.Name+": "+fc.goCallback(fv, op.Sig, goFails(op.Sig), site, recv))
	}
	return name + "{" + strings.Join(fields, ", ") + "}"
}

// goInterfaceKey is the naming key of the adapter of the Go interface to.
func goInterfaceKey(l *lowerer, to *check.Named) string {
	return "go interface " + l.typeKey(to)
}

// goFails reports a Go signature that returns an error last: its binding
// writes a fails row.
func goFails(sig *check.Signature) bool {
	return sig.Fallible || len(rowElems(sig.Errors)) > 0
}
