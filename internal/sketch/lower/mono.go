package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Monomorphic instances (MONO1; plan IR-G §3 item 13). A call of a generic
// function, or of a method of a generic record, whose type arguments are
// all known Go types goes to a plain copy of the callee for those type
// arguments: a Go function with no type parameter, so Go compiles it as
// written, with no GC-shape stencil and no dictionary. Inside a copy every
// type parameter is a Go type, so a generic call there whose type
// arguments name the copy's own parameters is a known instance too, and
// the copies close over the program's instances.
//
//   - The copy is keyed by the callee and the Go spelling of its
//     type-kinded arguments, so two instantiations whose substituted
//     bodies are equal (the same Go types) share one function, and the
//     count of copies is bounded by the distinct instances, never by the
//     call sites. Row arguments are erased in Go, so they key nothing: the
//     stenciled form shares one body across rows too.
//   - A method of a generic record is copied as a plain function that
//     takes its receiver first; its call passes the receiver (its address
//     for a *T method, as the method call does).
//   - An instance used as a value, Fn[int], names its copy the same way.
//   - The instances are finite: the checker refuses an instantiation
//     cycle, a polymorphic recursion, before lowering (S108,
//     check/mono.go), so no copy instantiates a growing argument.
//   - What stays stenciled: a call with a type argument that names a type
//     parameter of a generic caller that is not a copy, a callee with
//     owned parameters (its wrapper copies them, values.go V5), a call to
//     an owned body, a known-callback variant (known.go), a known-record
//     copy (record.go), a checked Close, and a native or extern body. A
//     test build takes copies too (MONO-HOOKS-MIN, shortcuts.go inTests):
//     tests run what ships, and a copy is compiled once, in its generic's
//     package, which spares every other package and shard of a split test
//     build a stencil of the instance (MONO-SHARED's aim, with no hook in
//     Go's compiler).
//
// A copy is the generic body lowered again with each type parameter bound
// to its Go type (typeCtx.mono): its //line directives, polls, failures
// and defects are the generic function's own, so a defect report names
// the same Ergo frames. A witness parameter (witness.go) is still passed
// as a value.
//
// The copy's name is its callee's reserved base (reserveMono, in the
// naming pass) with "_i" and the instance's number: the namer never gives
// a local or another package entity a name of that form (namer.taken), so
// no name the pass did not reserve can capture one.
//
// The reference is genericLowering "stencil", set by a test or by a build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.genericLowering=stencil:
// every generic call takes the Go generic function, and Go stencils it.
// TestGenericLoweringsAgree runs generated generic programs under both and
// against a model.
//
// Prior art: Rust, C++, MLton and MoonBit monomorphize every instance;
// .NET shares code among reference types and specializes value types; Go
// stencils by GC shape with a dictionary; Swift passes witness tables and
// specializes as an optimization; OCaml has one uniform representation.
// Gleam, ReScript and Elixir erase types on a dynamic host and have
// nothing to instantiate.
var genericLowering string

// monoKey is one copy: the declaration it instantiates and its Go type
// arguments.
type monoKey struct {
	decl monoDecl
	args string
}

// monoDecl is a declaration that has copies: a generic function
// (monoFunc), or a law of a generic operation type (monoLaw, G6), whose
// copy the law's run calls at each provider's instance.
type monoDecl interface {
	// base is the reserved naming-pass key of the declaration's copies.
	base() pkgKey
}

type monoFunc struct{ f *check.Func }

type monoLaw struct{ law *check.Law }

func (d monoFunc) base() pkgKey { return monoBase(d.f) }
func (d monoLaw) base() pkgKey  { return pkgKey{"mono", d.law} }

// monoBase is the reserved naming-pass key of f's copies.
func monoBase(f *check.Func) pkgKey { return pkgKey{"mono", f} }

// monoCandidate reports a function whose calls may go to a copy: generic
// (its own type-kinded parameters, or a method's receiver's), with an Ergo
// body, no extern, no native body, not a checked Close, not main, no
// owned parameter and no Go row (a Slot or held row).
func (l *lowerer) monoCandidate(f *check.Func) bool {
	if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil || f.Name == "main" {
		return false
	}
	generic := false
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType {
			generic = true
		}
	}
	// A Slot or held row is a Go type parameter (rowslot.go) that the
	// copy would erase: Bad[F] would be Bad[error] in the copy and
	// Bad[E0] at its caller. The function stays stenciled, as the
	// inlining and the known-record copies leave it.
	if !generic || isClose(f) || nativeOf(f) != nil || len(l.ownedParams(f)) > 0 || l.hasGoRow(f.Sig) {
		return false
	}
	return true
}

// reserveMono reserves, in the naming pass, the base name of the copies of
// every candidate function of pkgs: the function's name, after its
// receiver record's for a method.
func (l *lowerer) reserveMono(pkgs []*check.Package) {
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if !l.monoCandidate(f) {
				continue
			}
			base := f.Name
			if td, _ := l.methodReceiver(f); td != nil {
				base = td.tn.Name + strings.ToUpper(f.Name[:1]) + f.Name[1:]
			}
			name := l.pkgName(monoBase(f), base+"Mono")
			if l.names.monoBases == nil {
				l.names.monoBases = map[string]bool{}
			}
			l.names.monoBases[name] = true
		}
	}
}

// reserveLawMono reserves, in the test build's naming pass (tests.go),
// the base name of the copies of the law of a generic operation type law:
// the law's Go name, then Mono.
func (l *lowerer) reserveLawMono(law *check.Law) {
	name := l.pkgName(monoLaw{law}.base(), l.lawFunc(law)+"Mono")
	if l.names.monoBases == nil {
		l.names.monoBases = map[string]bool{}
	}
	l.names.monoBases[name] = true
}

// monoAt is the copy that a use of f at the type arguments args goes to,
// made and queued for emission at its first use; nil when the use takes
// the generic function. x and c are the call, or nil for an instance used
// as a value. It is the admission function of the shortcut mono-instance
// (shortcuts.go).
func (fc *funcCtx) monoAt(x *syntax.CallExpr, c *check.Call, f *check.Func, args []check.Type) *specCopy {
	if c != nil {
		if c.Kind != check.CallFunc || c.Func != f || c.Site != nil || c.StartsTask {
			return nil
		}
		if fc.l.vals != nil && fc.l.vals.ownCall[x] {
			return nil // the owned body (values.go, V5)
		}
	}
	if !fc.l.takes("mono-instance") {
		return nil
	}
	return fc.monoFor(f, args)
}

// monoFor is the copy of f for the type arguments args (the checker's, in
// f's type-parameter order), or nil when f has none or an argument names a
// type parameter that is no Go type here.
func (fc *funcCtx) monoFor(f *check.Func, args []check.Type) *specCopy {
	l := fc.l
	base, ok := l.pkgNames[monoBase(f)]
	if !ok {
		return nil
	}
	fd := l.funcDecl(f)
	if fd.extern != "" {
		return nil
	}
	typeArgs := map[*check.TypeParam]string{}
	bound := map[*check.TypeParam]check.Type{}
	var spelled []string
	for i, tp := range f.Sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(args) {
			return nil
		}
		if mentionsTypeParam(args[i]) && !fc.mono {
			return nil
		}
		g := fc.goType(args[i])
		typeArgs[tp] = g
		spelled = append(spelled, g)
		if t := fc.boundType(args[i]); !mentionsTypeParam(t) {
			bound[tp] = t
		}
	}
	if len(spelled) == 0 {
		return nil
	}
	sp, made := l.monoCopy(monoFunc{f}, base, spelled, f.Decl.Name)
	if made {
		sp.f, sp.fd, sp.typeArgs, sp.bound = f, fd, typeArgs, bound
		l.pending = append(l.pending, func() { l.emitBody(fd, false, sp, false) })
	}
	return sp
}

// monoCopy is the copy of decl at the Go type arguments spelled, named
// base_iN at its first use (made); its caller fills it and queues its
// body.
func (l *lowerer) monoCopy(decl monoDecl, base string, spelled []string, site syntax.Node) (sp *specCopy, made bool) {
	key := monoKey{decl, strings.Join(spelled, "\x00")}
	if sp := l.monos[key]; sp != nil {
		return sp, false
	}
	if l.monos == nil {
		l.monos = map[monoKey]*specCopy{}
		l.monoCount = map[monoDecl]int{}
	}
	l.monoCount[decl]++
	name := base + "_i" + strconv.Itoa(l.monoCount[decl])
	if l.names.used[name] {
		ice(site, "the copy name %s is a reserved package name", name)
	}
	l.names.used[name] = true
	sp = &specCopy{name: name, param: -1}
	l.monos[key] = sp
	l.took("mono-instance")
	return sp, true
}

// lawMonoFor is the copy of the law of a generic operation type law for
// the operation type's arguments targs (the instance a provider fixes),
// lowered by lawDecl with each type parameter bound to its Go type, or
// nil under the reference (genericLowering "stencil"), whose run calls
// the generic law at targs.
func (l *lowerer) lawMonoFor(law *check.Law, own *check.Named, targs []check.Type) *specCopy {
	if !l.takes("mono-instance") {
		return nil
	}
	base, ok := l.pkgNames[monoLaw{law}.base()]
	if !ok {
		ice(law.Decl, "the law %s has no reserved copy name (reserveLawMono)", law.Name)
	}
	tc := &typeCtx{l: l, site: law.Decl}
	typeArgs := map[*check.TypeParam]string{}
	bound := map[*check.TypeParam]check.Type{}
	var spelled []string
	for i, tp := range own.TParams {
		if i >= len(targs) {
			ice(law.Decl, "the law %s run at %d of its %d type arguments", law.Name, len(targs), len(own.TParams))
		}
		g := tc.goType(targs[i])
		typeArgs[tp] = g
		spelled = append(spelled, g)
		if !mentionsTypeParam(targs[i]) {
			bound[tp] = targs[i]
		}
	}
	sp, made := l.monoCopy(monoLaw{law}, base, spelled, law.Decl)
	if made {
		sp.typeArgs, sp.bound = typeArgs, bound
		l.pending = append(l.pending, func() { l.lawDecl(law, sp) })
	}
	return sp
}

// lawCallee is the Go function a run of the law of a generic operation
// type law calls at the instance targs: its copy, or the generic law at
// targs under the reference.
func (l *lowerer) lawCallee(law *check.Law, own *check.Named, targs []check.Type) string {
	if sp := l.lawMonoFor(law, own, targs); sp != nil {
		return sp.name
	}
	tc := &typeCtx{l: l, site: law.Decl}
	spelled := make([]string, len(targs))
	for i, a := range targs {
		spelled[i] = tc.goType(a)
	}
	return l.lawFunc(law) + "[" + strings.Join(spelled, ", ") + "]"
}

// isMonoName reports a name of the form base_iN for a reserved copy base:
// the namer gives such a name to no local and no package entity.
func (n *namer) isMonoName(name string) bool {
	i := strings.LastIndex(name, "_i")
	if i <= 0 || i+2 >= len(name) {
		return false
	}
	for _, r := range name[i+2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return n.monoBases[name[:i]]
}
