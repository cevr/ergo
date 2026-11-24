package lower

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// wireProjectionLowering selects singleton codec-witness projection or
// "off", the full-witness reference. A whole build can select the
// reference with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.wireProjectionLowering=off.
var wireProjectionLowering string

// SetWireProjectionForTest selects the reference and restores the old choice.
func SetWireProjectionForTest(mode string) func() {
	old := wireProjectionLowering
	wireProjectionLowering = mode
	return func() { wireProjectionLowering = old }
}

type wireProjectHead struct {
	pkg                *check.Package
	slot               int
	parts              []string
	directive, results string
}

// keepWireHead records a concrete function whose only witness is a codec.
// Its ordinary head is not changed; text and indirect callers still use it.
func (st *irState) keepWireHead(fc *funcCtx, f *check.Func, name, directive string, parts []string, results string) {
	if st.census != nil || st.formsOff || wireProjectionLowering == "off" {
		return
	}
	ps := witnessParams(f.Sig)
	if len(ps) != 1 || !isWireParam(ps[0]) || fc.static[ps[0]] != nil {
		return
	}
	slot := 0
	if fc.env != "" {
		slot++
	}
	if st.wireHeads == nil {
		st.wireHeads = map[string]wireProjectHead{}
	}
	st.wireHeads[name] = wireProjectHead{f.Pkg, slot, parts, directive, results}
}

// A witness projects only when every occurrence of its local selects
// the same callable field, or forwards the whole local to a concrete
// copy that transitively does so. Zero or multiple fields, an opaque
// use, mutation, and a recursive dependency keep the whole witness.
type wireProjection struct {
	head         wireProjectHead
	fn           *irFunc
	field, param string
	worker       string
	visited      bool
}

func witnessLocal(x *irExpr, slot int) bool {
	return x != nil && x.K == "local" && x.I == slot
}

func (p *wireProjection) demand(all map[string]*wireProjection) string {
	if p.visited {
		return p.field
	}
	p.visited = true
	slot := p.head.slot
	uses, admitted := 0, 0
	field := ""
	ok := true
	add := func(next string) {
		admitted++
		if next == "" || field != "" && field != next {
			ok = false
		}
		field = next
	}
	p.fn.visit(func(x *irExpr) {
		switch {
		case witnessLocal(x, slot):
			uses++
		case x.K == "field" && witnessLocal(x.X, slot):
			add(x.Name)
		case x.K == "call" && x.T == "":
			callee := all[x.Name]
			if callee != nil && callee.head.slot < len(x.Args) && witnessLocal(x.Args[callee.head.slot], slot) {
				add(callee.demand(all))
			}
		}
	}, func(s *irStmt) {
		if s.K == "set" && s.Local == slot {
			ok = false
		}
	})
	if ok && uses == admitted {
		p.param = wireProjectedParam(p.head.parts[slot], field)
		if p.param != "" {
			p.field = field
		}
	}
	return p.field
}

// wireProjectedParam reads the compiler-written anonymous witness type.
// Only a function-valued field can replace the full parameter.
func wireProjectedParam(part, name string) string {
	if name == "" {
		return ""
	}
	local, spelling, _ := strings.Cut(part, " ")
	x, err := parser.ParseExpr(spelling)
	st, ok := x.(*ast.StructType)
	if err != nil || !ok {
		return ""
	}
	for _, f := range st.Fields.List {
		if len(f.Names) == 1 && f.Names[0].Name == name {
			if _, ok := f.Type.(*ast.FuncType); !ok {
				return ""
			}
			var out strings.Builder
			out.WriteString(local + " ")
			if format.Node(&out, token.NewFileSet(), f.Type) == nil {
				return out.String()
			}
		}
	}
	return ""
}

func (st *irState) projectWireWitnesses(l *lowerer) {
	if len(st.wireHeads) == 0 {
		return
	}
	funcs := st.out.Funcs
	all := map[string]*wireProjection{}
	for i := range funcs {
		fn := &funcs[i]
		if h, ok := st.wireHeads[fn.Name]; ok {
			all[fn.Name] = &wireProjection{head: h, fn: fn}
		}
	}
	used := map[string]bool{}
	for i := range funcs {
		funcs[i].exprs(func(x *irExpr) {
			if p := all[x.Name]; x.K == "call" && x.T == "" && p != nil && p.demand(all) != "" {
				used[x.Name] = true
			}
		})
	}
	if len(used) == 0 {
		return
	}
	var copies []irFunc
	st.wireOrigins = map[string]*check.Package{}
	for i := range funcs {
		fn := &funcs[i]
		if !used[fn.Name] {
			continue
		}
		p := all[fn.Name]
		p.worker = l.names.alloc(fn.Name + "WitnessField")
		parts := slices.Clone(p.head.parts)
		parts[p.head.slot] = p.param
		l.gen.WriteString(p.head.directive + "func " + p.worker + "(" + strings.Join(parts, ", ") + ")" + p.head.results + "\n\n")
		st.wireOrigins[p.worker] = p.head.pkg
		st.report.IR = append(st.report.IR, p.worker)
		copy := *fn
		copy.Name = p.worker
		copy.Locals = slices.Clone(fn.Locals)
		copy.Body = (&irTreeCopy{}).stmts(fn.Body)
		copies = append(copies, copy)
	}
	rewrite := func(fn *irFunc, forwarded *wireProjection) {
		fn.exprs(func(x *irExpr) {
			if forwarded != nil && x.K == "field" && x.Name == forwarded.field && witnessLocal(x.X, forwarded.head.slot) {
				*x = irExpr{K: "local", Pos: x.Pos, I: forwarded.head.slot}
			}
			callee := all[x.Name]
			if x.K != "call" || x.T != "" || callee == nil || callee.worker == "" {
				return
			}
			at := callee.head.slot
			if at >= len(x.Args) {
				return
			}
			arg := x.Args[at]
			if forwarded == nil || !witnessLocal(arg, forwarded.head.slot) {
				x.Args = slices.Clone(x.Args)
				x.Args[at] = &irExpr{K: "field", Pos: arg.Pos, Name: callee.field, X: arg}
			}
			x.Name = callee.worker
		})
	}
	st.out.Funcs = append(st.out.Funcs, copies...)
	byWorker := map[string]*wireProjection{}
	for _, p := range all {
		if p.worker != "" {
			byWorker[p.worker] = p
		}
	}
	for i := range st.out.Funcs {
		fn := &st.out.Funcs[i]
		rewrite(fn, byWorker[fn.Name])
	}
}
