package lower

import (
	"fmt"
	"go/token"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
)

// Go calls Ergo (IG11; docs/spec/interop.md, Go calls Ergo; #31's host-driven
// programs). LowerLibrary lowers a package as a Go library package instead
// of a program: no Go main, and each exported function of the root package
// whose signature Go can spell becomes an exported Go function of the same
// name that a Go program (the host: a GUI loop, an HTTP server) calls:
//
//	func Hello(ctx context.Context, name string) (string, error)
//
// The Go function is the host entry of that call. It runs the Ergo
// function as a new root task on the calling goroutine (v1.Call), so a
// host that calls it on its locked main thread runs the Ergo code there
// (main-thread affinity: nothing moves the body to another goroutine; the
// tasks it starts run elsewhere and end before it returns). Its ctx cancels
// the call: a done ctx interrupts the task, its releases run, and the call
// returns ctx.Err(). The host provides what an entry provides (rows.md,
// Entry 4): Console, Ambient and go.Foreign; a function that needs another
// capability binds it with a with inside, as main does.
//
// What Go can spell: the basic types, slices of them, and Go's own types
// (a type of import go, a go.Ref of one, a borrow of one). A slice result
// crosses as a copy (the value rule: Go may write it, and it may share an
// Ergo value such as a package-level let); a slice argument crosses as is,
// since the Ergo call ends before Go's resumes and keeps nothing of it. An
// exported function whose signature names another type (an Ergo record, a
// union, a Map, a function), or that is generic, is not exported to Go;
// the emitted file lists each with its reason. The package-level lets run
// once, in the task of the first call.
//
// Prior art: Gleam and ReScript export every public function under its
// name in the host's module format (a JS export, an Erlang function);
// Kotlin/Native's framework export and cgo's //export write a C-callable
// wrapper per function; Effect's runtime.runPromise is the entry a host
// calls with an AbortSignal for interruption.

// LowerLibrary lowers the unit as the Go package name of the Go module
// module: go.mod, and the package's file name.go at the module's root,
// besides Runtime's files.
func LowerLibrary(u Unit, module, name string) ([]File, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	if !token.IsIdentifier(name) || name == "main" {
		return nil, fmt.Errorf("lower: %q is not a Go package name for a library", name)
	}
	l := newLowerer(u)
	l.lib = name
	code, err := l.program()
	if err != nil {
		return nil, err
	}
	files := []File{{Path: "go.mod", Data: goModFileAt(module, u.Go)}}
	if u.Go.Sum != "" {
		files = append(files, File{Path: "go.sum", Data: []byte(u.Go.Sum)})
	}
	return append(files, File{Path: name + ".go", Data: code}), nil
}

// goPackage is the emitted file's package name: main for a program.
func (l *lowerer) goPackage() string {
	if l.lib != "" {
		return l.lib
	}
	return "main"
}

// export is one exported function of a library, or the reason Go cannot
// call it.
type export struct {
	fn     *check.Func
	reason string
}

// exportsOf are the root package's exported functions with an Ergo body,
// in source order, each with the reason it is not exported to Go, if any.
func (l *lowerer) exportsOf() []export {
	var out []export
	for _, f := range l.canon.funcs(l.pkg) {
		if f.Decl == nil || f.Decl.Extern != nil || f.Decl.Body == nil {
			continue
		}
		if r, _ := utf8.DecodeRuneInString(f.Name); !unicode.IsUpper(r) {
			continue
		}
		out = append(out, export{fn: f, reason: exportReason(f)})
	}
	return out
}

// exportReason is why Go cannot call the exported function f, or "".
func exportReason(f *check.Func) string {
	sig := f.Sig
	if len(sig.TParams) > 0 {
		return "it is generic"
	}
	for i, p := range sig.Params {
		if !goSpells(p.Type) {
			return fmt.Sprintf("parameter %d has the type %s", i+1, p.Type)
		}
	}
	if len(sig.Results) > 1 {
		return "it has several results"
	}
	for _, r := range sig.Results {
		if !goSpells(r) {
			return "its result has the type " + r.String()
		}
	}
	for _, cap := range rowElems(sig.Uses) {
		if !hostProvides(cap) {
			return fmt.Sprintf("it needs %s, which a Go caller does not provide (bind it with a with inside)", cap)
		}
	}
	return ""
}

// goSpells reports an Ergo type whose Go type a Go caller can name.
func goSpells(t check.Type) bool {
	switch t := t.(type) {
	case *check.Basic:
		return true
	case *check.Slice:
		return goSpells(t.Elem)
	}
	return hostSourced(t)
}

// hostProvides reports a capability the host provides an entry: Console,
// Ambient or go.Foreign (rows.md, Entry 4).
func hostProvides(cap check.Type) bool {
	n, ok := cap.(*check.Named)
	if !ok {
		return false
	}
	if check.IsGoForeign(n) {
		return true
	}
	return n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude" && (n.Obj.Name == "Console" || n.Obj.Name == "Ambient")
}

// nameExports reserves, before every other package-scope name, each
// exported Go function's name (its Ergo name) and the library's imports.
func (l *lowerer) nameExports() {
	for _, e := range l.exportsOf() {
		if e.reason == "" {
			if got := l.pkgName(pkgKey{"export", e.fn}, e.fn.Name); got != e.fn.Name {
				ice(e.fn.Decl.Name, "the exported Go function %s was named %s", e.fn.Name, got)
			}
		}
	}
	l.pkgName(pkgKey{"import", "context"}, "context")
	l.pkgName(pkgKey{"import", "sync"}, "sync")
	l.pkgName(pkgKey{"once", nil}, "ergoInitOnce")
}

// exports writes the exported Go functions of a library and the host's
// providers of their capabilities.
func (l *lowerer) exports(b *strings.Builder) {
	rt := l.rt()
	ctx := l.pkgName(pkgKey{"import", "context"}, "context")
	l.libImports = append(l.libImports, ctx+` "context"`)
	host := l.pkgName(pkgKey{"host", "Console"}, "hostConsole")
	runInits := l.initOnce(b)
	var provided []string
	for _, e := range l.exportsOf() {
		f := e.fn
		if e.reason != "" {
			b.WriteString("// Not exported to Go: " + f.Name + ": " + e.reason + ".\n\n")
			continue
		}
		fd := l.funcDecl(f)
		tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: f.Decl.Name}
		locals := newNamer(l.names)
		ctxParam := locals.alloc("ctx")
		params := []string{ctxParam + " " + ctx + ".Context"}
		var args []string
		for i, p := range f.Sig.Params {
			n := locals.alloc("p" + strconv.Itoa(i))
			goT := tc.goType(p.Type)
			params = append(params, n+" "+goT)
			args = append(args, n)
		}
		var result check.Type
		resultGo := "struct{}"
		results := "error"
		if len(f.Sig.Results) == 1 {
			result = f.Sig.Results[0]
			resultGo = tc.goType(result)
			results = "(" + resultGo + ", error)"
		}
		var fields []string
		fields, provided = l.hostFields(f.Sig.Uses, host, provided, func(cap check.Type) {
			ice(f.Decl.Name, "the exported function %s needs %s, which exportReason admits", f.Name, cap)
		})
		b.WriteString("// " + f.Name + " calls the Ergo function " + f.Name + " for a Go caller (IG11): on the\n// calling goroutine, as a root task that ctx cancels (v1.Call).\n")
		b.WriteString("func " + f.Name + "(" + strings.Join(params, ", ") + ") " + results + " {\n")
		v := "v"
		if result == nil {
			v = "_"
		}
		body := l.rootTask(locals, "("+resultGo+", error)", fields, func(env string) string {
			call := fd.name + "(" + strings.Join(append(fd.envLead(env), args...), ", ") + ")"
			s := ""
			if fd.pure {
				s += "\t\t_ = " + env + "\n"
			}
			s += runInits(env)
			switch {
			case result == nil && fd.fallible:
				return s + "\t\treturn struct{}{}, " + call + "\n"
			case result == nil:
				return s + "\t\t" + call + "\n\t\treturn struct{}{}, nil\n"
			case fd.fallible:
				return s + "\t\treturn " + call + "\n"
			}
			return s + "\t\treturn " + call + ", nil\n"
		})
		b.WriteString("\t" + v + ", err := " + rt + ".Call(" + ctxParam + ", " + strconv.Quote(f.Name) + ", " + body + ")\n")
		if result == nil {
			b.WriteString("\treturn err\n}\n\n")
			continue
		}
		if _, ok := result.(*check.Slice); ok {
			b.WriteString("\treturn append(" + resultGo + "(nil), v...), err\n}\n\n")
			continue
		}
		b.WriteString("\treturn v, err\n}\n\n")
	}
	b.WriteString(l.hostDecl(host, provided, "the exported functions take"))
}

// initOnce declares the sync.Once that runs a library's package-level lets
// once, in the task of the first call, and returns the statement that runs
// them given the root Env's name ("" when the package has none). It is
// apart from exports, which emits runtime calls: Once.Do is sync's, not a
// runtime binding (TestRuntime199BlockingOwner).
func (l *lowerer) initOnce(b *strings.Builder) func(env string) string {
	once := l.pkgName(pkgKey{"once", nil}, "ergoInitOnce")
	if !l.hasInits() {
		return func(string) string { return "" }
	}
	// The exported functions call ergoInit through the Once (F13's check
	// in program).
	l.entryInits = true
	sync := l.pkgName(pkgKey{"import", "sync"}, "sync")
	l.libImports = append(l.libImports, sync+` "sync"`)
	b.WriteString("// " + once + " runs the package-level lets once, in the task of the first call.\nvar " + once + " " + sync + ".Once\n\n")
	return func(env string) string {
		return "\t\t" + once + ".Do(func() { " + l.pkgName(pkgKey{"init", nil}, "ergoInit") + "(" + env + ") })\n"
	}
}
