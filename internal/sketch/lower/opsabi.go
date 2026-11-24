package lower

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// opsABIKey is one Go operation-interface shape of a declaration. Only
// empty failure slots need a distinct shape; nonempty rows share the
// original Go error ABI. The value-kinded arguments are still Go generics.
type opsABIKey struct {
	origin *check.TypeName
	empty  string
}

func opsABIOrigin(n *check.Named) *check.Named {
	if n.Orig != nil {
		n = n.Orig
	}
	if n.Obj.Pkg.Host() || check.ConstraintOnly(n) {
		return nil
	}
	if _, ok := check.GoTypeOf(n); ok {
		return nil
	}
	if _, ok := n.Underlying().(*check.Interface); !ok {
		return nil
	}
	return n
}

// opsABIRows reads the parameters that can erase a method's entire
// failure result. A method with a fixed error always takes the pair ABI.
func opsABIRows(n *check.Named) map[*check.TypeParam]bool {
	n = opsABIOrigin(n)
	if n == nil {
		return nil
	}
	var rows map[*check.TypeParam]bool
	for _, m := range n.Underlying().(*check.Interface).Methods {
		es := rowElems(m.Sig.Errors)
		erasable := len(es) != 0
		for _, e := range es {
			tp, ok := e.(*check.TypeParam)
			if !ok || tp.Kind != check.KindErrors {
				erasable = false
				break
			}
		}
		if erasable {
			for _, e := range es {
				if rows == nil {
					rows = map[*check.TypeParam]bool{}
				}
				rows[e.(*check.TypeParam)] = true
			}
		}
	}
	return rows
}

func emptyRowArg(t check.Type) bool {
	r, ok := t.(*check.RowArg)
	return ok && r.Row.Empty()
}

func opsABI(n *check.Named) (opsABIKey, map[*check.TypeParam]check.Type) {
	orig := n.Orig
	if orig == nil {
		return opsABIKey{}, nil
	}
	rows := opsABIRows(orig)
	var key strings.Builder
	var bindings map[*check.TypeParam]check.Type
	for i, p := range orig.TParams {
		if rows[p] && emptyRowArg(n.TArgs[i]) {
			if bindings == nil {
				bindings = map[*check.TypeParam]check.Type{}
			}
			bindings[p] = n.TArgs[i]
			key.WriteString(strconv.Itoa(i))
			key.WriteByte('_')
		}
	}
	if len(bindings) == 0 {
		return opsABIKey{}, nil
	}
	return opsABIKey{origin: orig.Obj, empty: key.String()}, bindings
}

// opsABIName queues a Go declaration for the selected row shape. The
// checker's substitution reads the methods together, so includes and
// ordinary generic values in their parameters retain their identities.
func (l *lowerer) opsABIName(n *check.Named, td *typeDecl, site syntax.Node) string {
	shape, bound := opsABI(n)
	if len(bound) == 0 {
		return l.useType(td).name
	}
	key := pkgKey{"ops ABI", shape}
	if name, ok := l.pkgNames[key]; ok {
		return name
	}
	name := l.pkgName(key, td.name+"Rows"+shape.empty)
	l.pending = append(l.pending, func() {
		defer l.leave(l.enter(site, td.tn.Pkg))
		tparams, names := l.typeParams(td.tparams, newNamer(l.names), site)
		tc := &typeCtx{l: l, params: names, site: site}
		read := check.CopyReader(td.tn.Pkg, bound)
		var out strings.Builder
		out.WriteString("type " + name + tparams + " interface {\n")
		for _, op := range td.ops {
			out.WriteString("\t" + memberName(op.Name) + tc.methodSig(read(op.Sig).(*check.Signature)) + "\n")
		}
		out.WriteString("}\n\n")
		l.gen.WriteString(out.String())
	})
	return name
}

// opABIBindings finds a function's row parameters that can change the Go
// type of a parameter or result. An instantiation closing one such row
// uses a checked copy rather than calling the open ABI by mistake.
func opABIBindings(sig *check.Signature) map[*check.TypeParam]bool {
	var rows map[*check.TypeParam]bool
	walkType(sig, func(t check.Type, _ bool) bool {
		n, ok := t.(*check.Named)
		if !ok || n.Orig == nil {
			return false
		}
		need := opsABIRows(n.Orig)
		for i, tp := range n.Orig.TParams {
			r, ok := n.TArgs[i].(*check.RowArg)
			if !need[tp] || !ok {
				continue
			}
			for _, e := range rowElems(r.Row) {
				if p, ok := e.(*check.TypeParam); ok && p.Kind == check.KindErrors {
					if rows == nil {
						rows = map[*check.TypeParam]bool{}
					}
					rows[p] = true
				}
			}
		}
		return false
	})
	return rows
}

func opABICopy(sig *check.Signature, args []check.Type) bool {
	rows := opABIBindings(sig)
	for i, p := range sig.TParams {
		if i < len(args) && rows[p] && emptyRowArg(args[i]) {
			return true
		}
	}
	return false
}

func openRowArg(t check.Type) bool {
	r, ok := t.(*check.RowArg)
	if !ok {
		return false
	}
	for _, e := range rowElems(r.Row) {
		if _, ok := e.(*check.TypeParam); ok {
			return true
		}
	}
	return false
}

func rowWidening(c check.Conversion) bool {
	from, fok := c.From.(*check.Named)
	to, tok := c.To.(*check.Named)
	if !fok || !tok || from.Orig == nil || from.Orig != to.Orig {
		return false
	}
	a, _ := opsABI(from)
	b, _ := opsABI(to)
	return a != b
}

// widenOps adapts an erased operation value only when its ABI changes.
// The checked row subset guarantees every differing method is widening
// from one result to a result plus nil error, never the reverse.
func (fc *funcCtx) widenOps(c check.Conversion, site syntax.Node) string {
	l := fc.l
	base, stem, _ := l.adapterName(c)
	key := pkgKey{"adapter", base}
	name := l.pkgName(key, stem)
	free := convParams(c)
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	var params, names, actual []string
	naming := newNamer(l.names)
	for _, tp := range free {
		p := naming.alloc(tp.Name)
		tc.params[tp] = p
		params = append(params, p+" "+l.constraint(tp, site))
		names = append(names, p)
		actual = append(actual, fc.goType(tp))
	}
	brackets := func(xs []string) string {
		if len(xs) == 0 {
			return ""
		}
		return "[" + strings.Join(xs, ", ") + "]"
	}
	if l.adapters[base] != "" {
		return name + brackets(actual)
	}
	l.adapters[base] = name
	from := c.From.(*check.Named).Underlying().(*check.Interface)
	to := c.To.(*check.Named).Underlying().(*check.Interface)
	l.gen.WriteString("type " + name + brackets(params) + " struct{ v " + tc.goType(c.From) + " }\n")
	for _, op := range to.Methods {
		params := []string{"task *" + l.taskPkg() + ".Task"}
		args := []string{"task"}
		for i, p := range op.Sig.Params {
			arg := fmt.Sprintf("p%d", i)
			params = append(params, arg+" "+tc.goType(p.Type))
			args = append(args, arg)
		}
		var result check.Type
		if len(op.Sig.Results) != 0 {
			result = op.Sig.Results[0]
		}
		fails := !op.Sig.Errors.Empty()
		sourceFails := false
		for _, old := range from.Methods {
			if old.Name == op.Name {
				sourceFails = !old.Sig.Errors.Empty()
				break
			}
		}
		member := memberName(op.Name)
		call := "a.v." + member + "(" + strings.Join(args, ", ") + ")"
		var body string
		switch {
		case fails && !sourceFails && result != nil:
			body = "return " + call + ", nil"
		case fails && !sourceFails:
			body = call + "\n\treturn nil"
		case result != nil || fails:
			body = "return " + call
		default:
			body = call
		}
		l.gen.WriteString("func (a *" + name + brackets(names) + ") " + member + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, fails) + " {\n\t" + body + "\n}\n")
	}
	return name + brackets(actual)
}
