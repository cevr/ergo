package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Tabulated decisions (VI1). A pure function over a finite domain, each
// parameter an enum (a union whose variants have no fields), a bool or a
// byte, whose body decides (an if or a match) and whose result holds no
// reference (no slice, Map, function, pointer or operation value), is
// called through a table of its results by the index of its arguments:
// a parser's Next(state, class) or ClassOf(byte), a switch Go compiles to
// jumps and compares, becomes one load. The table holds at most tabMax
// entries; each fills at its first call, from the function itself, so an
// argument the program never passes is never computed, and a call that
// raises a defect leaves its entry empty and raises it again at the next
// call, as the function would (the table changes no result and no
// defect: the function is pure, so each entry is its value). One task
// writes an entry and publishes it with an atomic store (v1.MemoFlag);
// another that finds it unwritten calls the function.
//
// The direct call wins first: a call the inliner inlines is not
// tabulated, nor a function with no decision (a constructor such as
// to(s, a) is cheaper than a load).
//
// The reference is tabLowering "call", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.tabLowering=call:
// every call calls the function. TestTabulatedAgree runs generated
// programs under both.
//
// Prior art: LLVM's SwitchToLookupTable and GCC's switch conversion turn
// a switch whose cases yield constants into a table load; lexer
// generators (re2c, Ragel) and hand parsers (the vt control's table, Paul
// Williams's DEC parser) tabulate the transition function; GHC and
// MLton memoize nothing implicitly. Gleam, Elixir and ReScript leave a
// case expression to the host (the BEAM's select_val jump table, a JS
// switch). Here the table is built from the function itself, lazily, so
// it needs no constant evaluation.
var tabLowering string

// tabMax bounds a table's entries.
const tabMax = 4096

// tabInfo is a function's domain: each parameter's size and kind.
type tabInfo struct {
	params []tabParam
	size   int
}

type tabParam struct {
	t    check.Type
	n    int
	kind string // "enum", "bool" or "byte"
}

// tabulable is f's domain when its calls go through a table, else nil
// (memoized).
func (l *lowerer) tabulable(f *check.Func) *tabInfo {
	if tabLowering == "call" {
		return nil
	}
	if ti, ok := l.tabs[f]; ok {
		return ti
	}
	if l.tabs == nil {
		l.tabs = map[*check.Func]*tabInfo{}
	}
	ti := l.findTab(f)
	l.tabs[f] = ti
	return ti
}

func (l *lowerer) findTab(f *check.Func) *tabInfo {
	d := f.Decl
	if d == nil || d.Body == nil || d.Extern != nil || d.Recv != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Name == "main" || isClose(f) {
		return nil
	}
	if len(f.Sig.TParams) > 0 || len(f.Sig.Results) != 1 || len(f.Sig.Params) == 0 || !l.isPure(f) {
		return nil
	}
	if !refFree(f.Sig.Results[0], map[check.Type]bool{}) {
		return nil
	}
	decides := false
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		switch n.(type) {
		case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
			decides = true
		}
		return !decides
	})
	if !decides {
		return nil
	}
	ti := &tabInfo{size: 1}
	for _, p := range f.Sig.Params {
		tp := tabParamOf(p.Type)
		if tp == nil {
			return nil
		}
		ti.size *= tp.n
		if ti.size > tabMax {
			return nil
		}
		ti.params = append(ti.params, *tp)
	}
	return ti
}

// tabParamOf is t as a finite domain, or nil.
func tabParamOf(t check.Type) *tabParam {
	switch x := t.(type) {
	case *check.Basic:
		switch x.Kind {
		case check.Bool:
			return &tabParam{t: t, n: 2, kind: "bool"}
		case check.Uint8:
			return &tabParam{t: t, n: 256, kind: "byte"}
		}
	case *check.Named:
		if len(x.TArgs) > 0 {
			return nil
		}
		u, ok := x.Underlying().(*check.Union)
		if !ok || u.Row != nil || u.Bad || len(u.Variants) < 2 || len(u.Variants) > 256 {
			return nil
		}
		for _, v := range u.Variants {
			vn, ok := v.(*check.Named)
			if !ok {
				return nil
			}
			if s, ok := vn.Underlying().(*check.Struct); !ok || len(s.Fields) > 0 {
				return nil
			}
		}
		return &tabParam{t: t, n: len(u.Variants), kind: "enum"}
	}
	return nil
}

// refFree reports a type whose values hold no reference: no slice, Map,
// function, pointer, operation value or runtime handle, so a table entry
// shares nothing with a caller's value.
func refFree(t check.Type, seen map[check.Type]bool) bool {
	if seen[t] {
		return true
	}
	seen[t] = true
	switch x := t.(type) {
	case *check.Basic:
		return x.Kind != check.Invalid
	case *check.Named:
		switch u := x.Underlying().(type) {
		case *check.Struct:
			for _, f := range u.Fields {
				if !refFree(f.Type, seen) {
					return false
				}
			}
			return true
		case *check.Union:
			if u.Row != nil || u.Bad {
				return false
			}
			for _, v := range u.Variants {
				if !refFree(v, seen) {
					return false
				}
			}
			return true
		case *check.Basic:
			return refFree(u, seen)
		}
	}
	return false
}

// tabAt writes the read of the call c of a tabulated function with the
// arguments vals, and is its value; ok is false when c takes the
// function itself. The read is statements, not a helper function: Go
// would not inline a helper that also calls the fill (its call costs most
// of the inliner's budget), so the table would cost a call more than the
// function. Each argument is evaluated once, in order, into a temporary.
func (fc *funcCtx) tabAt(c *check.Call, vals []string) (string, bool) {
	if c.Kind != check.CallFunc || c.Func == nil || c.Recv != nil || c.StartsTask || fc.out == nil {
		return "", false
	}
	l := fc.l
	ti := l.tabulable(c.Func)
	if ti == nil || len(vals) != len(ti.params) {
		return "", false
	}
	for _, p := range ti.params {
		if p.kind != "enum" {
			continue
		}
		fu := l.flatUnionOf(p.t)
		if fu == nil || fu.generic || fu.niche {
			return "", false
		}
		for _, s := range fu.slot {
			if s {
				return "", false
			}
		}
	}
	flags, ok := l.pkgNames[pkgKey{"tabK", c.Func}]
	if !ok {
		return "", false
	}
	vs, fill := l.pkgNames[pkgKey{"tabV", c.Func}], l.pkgNames[pkgKey{"tabFill", c.Func}]
	if l.tabEmitted == nil {
		l.tabEmitted = map[*check.Func]bool{}
	}
	if !l.tabEmitted[c.Func] {
		l.tabEmitted[c.Func] = true
		l.stats.Tabulated++
		l.gen.WriteString(fc.tabDecl(flags, vs, fill, l.funcDecl(c.Func).name, c.Func, ti))
	}
	args := make([]string, len(vals))
	for k, v := range vals {
		args[k] = fc.temp("a")
		fc.line(args[k] + " := " + v)
	}
	i := fc.temp("i")
	fc.line(i + " := 0")
	for k, p := range ti.params {
		n := strconv.Itoa(p.n)
		switch p.kind {
		case "enum":
			fc.line(i + " = " + i + "*" + n + " + int(" + args[k] + ".tag)")
		case "byte":
			fc.line(i + " = " + i + "*" + n + " + int(" + args[k] + ")")
		case "bool":
			fc.line(i + " *= 2")
			fc.line("if " + args[k] + " {")
			fc.line("\t" + i + "++")
			fc.line("}")
		}
	}
	v := fc.temp("v")
	fc.line("var " + v + " " + fc.goType(c.Func.Sig.Results[0]))
	fc.line("if " + flags + "[" + i + "].Ready() {")
	fc.line("\t" + v + " = " + vs + "[" + i + "]")
	fc.line("} else {")
	fc.line("\t" + v + " = " + fill + "(" + i + ", " + strings.Join(args, ", ") + ")")
	fc.line("}")
	return v, true
}

// tabDecl is the table of f (Go name fname): its flags, its values and
// the function that fills an entry.
func (fc *funcCtx) tabDecl(flags, vals, fill, fname string, f *check.Func, ti *tabInfo) string {
	var params, args []string
	for k, p := range ti.params {
		a := "a" + strconv.Itoa(k)
		params = append(params, a+" "+fc.goType(p.t))
		args = append(args, a)
	}
	res := fc.goType(f.Sig.Results[0])
	size := strconv.Itoa(ti.size)
	call := strings.Join(args, ", ")
	var b strings.Builder
	b.WriteString("// " + vals + " are " + f.Name + "'s results by the index of its arguments, each filled at its first call (tabulate.go).\n")
	b.WriteString("var " + flags + " [" + size + "]" + fc.l.rt() + ".MemoFlag\n\n")
	b.WriteString("var " + vals + " [" + size + "]" + res + "\n\n")
	b.WriteString("func " + fill + "(i int, " + strings.Join(params, ", ") + ") " + res + " {\n")
	b.WriteString("\tv := " + fname + "(" + call + ")\n")
	b.WriteString("\tif " + flags + "[i].Claim() {\n\t\t" + vals + "[i] = v\n\t\t" + flags + "[i].Done()\n\t}\n\treturn v\n}\n\n")
	return b.String()
}

// reserveTabs gives, in the naming pass, the Go name of the table of each
// function of pkgs that has one.
func (l *lowerer) reserveTabs(pkgs []*check.Package) {
	if tabLowering == "call" {
		return
	}
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if l.tabulable(f) != nil {
				l.pkgName(pkgKey{"tabK", f}, "tab"+f.Name+"K")
				l.pkgName(pkgKey{"tabV", f}, "tab"+f.Name+"V")
				l.pkgName(pkgKey{"tabFill", f}, "tab"+f.Name+"Fill")
			}
		}
	}
}
