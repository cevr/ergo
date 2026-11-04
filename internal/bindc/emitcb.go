package bindc

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The callbacks of the Go package (GoPackage). Each bound callback typedef
// has one exported Go func type, one trampoline and one registry map:
//
//   - the trampoline cbTramp<n> is a Go function in the exact C shape of
//     the typedef. Load makes it into a C pointer with purego.NewCallback
//     once per process (cbMake; never per Load, registration or call), so
//     the binding uses at most one purego callback slot per bound typedef,
//     and Bind bounds those at MaxBoundCallbacks;
//   - the registry map cbMap<n> maps the contract's key to a cbEntry (the
//     Go function and, for a registered callback, its owner's C address). A
//     token key is a uintptr from a counter (cbToken): an integer, never a
//     Go pointer, never reused. A handle key is the struct cbKey<n> of the
//     key's fields. One RWMutex (cbMu) guards every map; no lock is held
//     while Go code of a callback runs, so a callback may call the binding
//     again;
//   - per invocation, the trampoline takes the read lock, looks up its key,
//     releases the lock and calls the Go function directly: no allocation,
//     no reflection, and one indirect call (the Go function), as a
//     hand-written purego callback with the same contract has. With no
//     entry, or a NULL pointer where C must pass one, C gets the entry's
//     default, no Go code runs and no out position is written.
//
// The order against C: a typed call puts the entry before the C call that
// installs the callback (a probe during that call finds it); a replacement
// keeps the entry it replaced and puts it back when the C call fails; a
// clear (a nil func) removes the entry after the C call returns success
// (a probe during that call finds the old one); an end function removes the
// entry of its key, and the owner's destroy every entry of the owner, after
// a successful return; a call-scoped entry is removed when its call
// returns, on every path (defer). A failed C call leaves the registry as
// it was.

// boundCbs are the bound callback typedefs, in Index order.
func (e *goEmitter) boundCbs() []*BoundCallback {
	var out []*BoundCallback
	for _, cb := range e.b.Callbacks {
		if cb.Bound() {
			out = append(out, cb)
		}
	}
	return out
}

// cbFuncType emits the Go func type of a bound callback typedef.
func (e *goEmitter) cbFuncType(cb *BoundCallback) {
	r := cb.Rule
	e.p("// %s is the Go form of the C callback %s.\n", cb.GoName, cb.Decl.Name)
	switch r.Scope {
	case ScopeCall:
		e.p("// C calls it only during the call that passes it.\n")
	default:
		e.p("// C keeps it until a replacement, a nil func, ")
		if len(r.Ends) > 0 {
			e.p("a successful\n// %s of its key, ", strings.Join(r.Ends, " or "))
		}
		e.p("or the destroy of its %s.\n", r.Owner)
	}
	for _, p := range cb.Sig.Params {
		if p.Class == ClassInBuffer {
			e.p("// %s is borrowed C memory: it is valid only during the call, and\n// the func must not retain it.\n", p.Name)
		}
	}
	if def := string(r.Default); def != "" {
		e.p("// When no Go function runs for an invocation, C gets %s.\n", def)
	} else {
		e.p("// When no Go function runs for an invocation, C gets no result.\n")
	}
	e.p("type %s %s\n\n", cb.GoName, cb.Sig.String())
}

// cbKeyFields are the parameter indexes of the key of cb, in the key's
// order.
func cbKeyFields(cb *BoundCallback) []int {
	var out []int
	for _, n := range cb.Rule.Key.Params {
		j, _ := namedParam(cb.Decl.Params, n)
		out = append(out, j)
	}
	return out
}

// cbKeyType is the Go key type of the registry of cb: uintptr for a token,
// else cbKey<n>.
func cbKeyType(cb *BoundCallback) string {
	if cb.Token {
		return "uintptr"
	}
	return "cbKey" + strconv.Itoa(cb.Index)
}

// callbacks emits the callback section: the slot error, the registry and
// its helpers, the trampolines and LiveCallbacks. A binding without a
// bound callback has none of it.
func (e *goEmitter) callbacks() {
	cbs := e.boundCbs()
	if len(cbs) == 0 {
		return
	}
	pkg := e.b.Contract.Go.Package
	fmtPkg, pg := e.use("fmt"), e.use("github.com/ebitengine/purego")
	e.use("sync")
	e.use("sync/atomic")
	e.p("// CallbackSlotsError is the error of Load when purego has no callback\n// slot left for the trampoline of a callback typedef (purego has 2000 per\n// process and never frees one). Load then leaves the package unloaded.\n")
	e.p("type CallbackSlotsError struct {\n\t// Typedef is the C callback typedef.\n\tTypedef string\n\t// Value is purego's panic value.\n\tValue any\n}\n\n")
	e.p("func (e *CallbackSlotsError) Error() string {\n\treturn %s.Sprintf(%q, e.Typedef, e.Value)\n}\n\n", fmtPkg,
		pkg+": Load: no purego callback slot for the trampoline of %s: %v")
	e.p("// cbEntry is one registration of a callback: its Go function and, for a\n// registered callback, the C address of its owner handle.\ntype cbEntry[F any] struct {\n\tfn    F\n\towner uintptr\n}\n\n")
	for _, cb := range cbs {
		if cb.Token {
			continue
		}
		e.p("// cbKey%d is the key of %s: (%s).\ntype cbKey%d struct {\n", cb.Index, cb.Decl.Name, strings.Join(cb.Rule.Key.Params, ", "), cb.Index)
		for i, j := range cbKeyFields(cb) {
			e.p("\tk%d %s\n", i, e.rawType(cb.Decl.Params[j].Type))
		}
		e.p("}\n\n")
	}
	e.p("// The registry: one map per bound callback typedef, keyed by the\n// contract's key, under cbMu. No lock is held while Go code of a callback\n// runs.\nvar (\n")
	e.p("\tcbMu sync.RWMutex\n\t// cbNext is the last token given.\n\tcbNext atomic.Uintptr\n")
	for _, cb := range cbs {
		e.p("\tcbMap%d = map[%s]cbEntry[%s]{}\n", cb.Index, cbKeyType(cb), cb.GoName)
	}
	e.p("\t// cbPtr are the C pointers of the trampolines, made once per process.\n\tcbPtr [%d]uintptr\n)\n\n", len(cbs))
	e.p("// cbFuncs are the trampolines, in cbPtr order, with their C typedefs.\nvar cbFuncs = [...]struct {\n\tname string\n\tfn   any\n}{\n")
	for _, cb := range cbs {
		e.p("\t{%q, cbTramp%d},\n", cb.Decl.Name, cb.Index)
	}
	e.p("}\n\n")
	e.p(`// cbMake makes the C pointer of every trampoline that has none yet, with
// purego.NewCallback: once per process, never per Load, registration or call.
func cbMake() error {
	for i := range cbFuncs {
		if cbPtr[i] != 0 {
			continue
		}
		p, err := cbNew(cbFuncs[i].name, cbFuncs[i].fn)
		if err != nil {
			return err
		}
		cbPtr[i] = p
	}
	return nil
}

// cbNew makes one C pointer; a panic of purego (no slot left) is a
// *CallbackSlotsError.
func cbNew(name string, fn any) (p uintptr, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &CallbackSlotsError{Typedef: name, Value: v}
		}
	}()
	return %[1]s.NewCallback(fn), nil
}

// cbToken gives a new token: an integer from a counter, never a Go pointer
// and never reused.
func cbToken() uintptr { return cbNext.Add(1) }

// cbPut sets the entry of k and returns the entry it replaced.
func cbPut[K comparable, F any](m map[K]cbEntry[F], k K, e cbEntry[F]) (old cbEntry[F], had bool) {
	cbMu.Lock()
	old, had = m[k]
	m[k] = e
	cbMu.Unlock()
	return old, had
}

// cbUndo puts back the entry that cbPut replaced, or removes k when it
// replaced none.
func cbUndo[K comparable, F any](m map[K]cbEntry[F], k K, old cbEntry[F], had bool) {
	cbMu.Lock()
	if had {
		m[k] = old
	} else {
		delete(m, k)
	}
	cbMu.Unlock()
}

// cbDrop removes the entry of k.
func cbDrop[K comparable, F any](m map[K]cbEntry[F], k K) {
	cbMu.Lock()
	delete(m, k)
	cbMu.Unlock()
}

// cbDropOwner removes every entry of the owner handle at C address owner.
func cbDropOwner[K comparable, F any](m map[K]cbEntry[F], owner uintptr) {
	cbMu.Lock()
	for k, e := range m {
		if e.owner == owner {
			delete(m, k)
		}
	}
	cbMu.Unlock()
}

`, pg)
	var lens []string
	for _, cb := range cbs {
		lens = append(lens, "len(cbMap"+strconv.Itoa(cb.Index)+")")
	}
	e.p("// LiveCallbacks is the number of live registrations: a call-scoped\n// callback during its call, and a registered one until a replacement, a\n// clear, an end or its owner's destroy releases it.\nfunc LiveCallbacks() int {\n\tcbMu.RLock()\n\tdefer cbMu.RUnlock()\n\treturn %s\n}\n\n", strings.Join(lens, " + "))
	for _, cb := range cbs {
		e.trampoline(cb)
	}
}

// trampoline emits cbTramp<n>, the one Go function in the C shape of cb.
func (e *goEmitter) trampoline(cb *BoundCallback) {
	d, r, n := cb.Decl, cb.Rule, cb.Index
	cf := callbackFunc(d)
	keyAt := map[int]bool{}
	for _, j := range cbKeyFields(cb) {
		keyAt[j] = true
	}
	a := func(i int) string { return "_a" + strconv.Itoa(i) }
	var params []string
	for i, p := range d.Params {
		t := e.rawType(p.Type)
		if cb.Token && keyAt[i] {
			t = "uintptr"
		}
		params = append(params, a(i)+" "+t)
	}
	res := ""
	switch cf.Result.Kind {
	case TypeVoid:
	case TypePointer:
		res = " uintptr"
	default:
		res = " " + e.rawType(cf.Result)
	}
	var key string
	if cb.Token {
		key = a(cbKeyFields(cb)[0])
	} else {
		var ks []string
		for _, j := range cbKeyFields(cb) {
			ks = append(ks, a(j))
		}
		key = cbKeyType(cb) + "{" + strings.Join(ks, ", ") + "}"
	}
	def := ""
	if res != "" {
		def = " " + string(r.Default)
	}
	e.p("// cbTramp%d is the trampoline of %s: it runs the Go function of its\n// key (%s) in cbMap%d. With none, or with a NULL pointer where C\n// must pass one, no Go code runs, no out position is written and C gets", n, d.Name, strings.Join(r.Key.Params, ", "), n)
	if def != "" {
		e.p("%s.\n", def)
	} else {
		e.p(" no result.\n")
	}
	e.p("func cbTramp%d(%s)%s {\n", n, strings.Join(params, ", "), res)
	e.p("\tcbMu.RLock()\n\t_e := cbMap%d[%s]\n\tcbMu.RUnlock()\n", n, key)
	conds := []string{"_e.fn == nil"}
	pos := map[int]Position{}
	for _, q := range cb.Positions {
		pos[q.Param] = q
		switch q.Class {
		case ClassValue, ClassOut, ClassOutArray:
			conds = append(conds, a(q.Param)+" == nil")
		case ClassInBuffer:
			conds = append(conds, a(q.Param)+" == nil && "+a(q.Length)+" != 0")
		}
	}
	e.p("\tif %s {\n\t\treturn%s\n\t}\n", strings.Join(conds, " || "), def)
	var args []string
	for _, gp := range cb.Sig.Params {
		q := pos[gp.C]
		p := d.Params[gp.C]
		switch q.Class {
		case ClassScalar:
			if rt := e.rawType(p.Type); rt != gp.Type {
				args = append(args, gp.Type+"("+a(gp.C)+")")
			} else {
				args = append(args, a(gp.C))
			}
		case ClassValue:
			args = append(args, "*(*"+gp.Type+")("+a(gp.C)+")")
		case ClassInBuffer:
			ln := a(q.Length)
			if es := e.elemSize(*p.Type.Elem); q.Bytes && es != 1 {
				ln += "/" + strconv.FormatInt(es, 10)
			}
			args = append(args, "unsafe.Slice((*"+strings.TrimPrefix(gp.Type, "[]")+")("+a(gp.C)+"), "+ln+")")
		}
	}
	call := "_e.fn(" + strings.Join(args, ", ") + ")"
	if len(cb.Sig.Results) == 0 {
		e.p("\t%s\n}\n\n", call)
		return
	}
	var vs []string
	for i := range cb.Sig.Results {
		vs = append(vs, "_v"+strconv.Itoa(i))
	}
	e.p("\t%s := %s\n", strings.Join(vs, ", "), call)
	ret := ""
	for i, gr := range cb.Sig.Results {
		if gr.C < 0 {
			if rt := strings.TrimSpace(res); rt != gr.Type {
				ret = rt + "(" + vs[i] + ")"
			} else {
				ret = vs[i]
			}
			continue
		}
		e.p("\t*(*%s)(%s) = %s\n", gr.Type, a(gr.C), vs[i])
	}
	if ret != "" {
		e.p("\treturn %s\n", ret)
	}
	e.p("}\n\n")
}

// cbUse is one callback parameter of a typed function.
type cbUse struct {
	i         int // the C parameter
	cb        *BoundCallback
	fn        string // the Go parameter (the func)
	key       string // the Go expression of the key, a local _k<i>
	ownerAddr string // the Go expression of the owner's C address (registered)
}

// cbUses are the callback parameters of the typed function f, in C order,
// with their keys; goParam maps a C parameter to its Go parameter. A key
// source that is not a value parameter is an error of the emitter.
func (e *goEmitter) cbUses(f *BoundFunc, goParam map[int]GoParam) []cbUse {
	k := e.b.k
	var out []cbUse
	for _, q := range f.Positions {
		if q.Class != ClassCallback {
			continue
		}
		cb := k.cbs[f.C.Params[q.Param].Type.Name]
		u := cbUse{i: q.Param, cb: cb, fn: goParam[q.Param].Name, key: "_k" + strconv.Itoa(q.Param)}
		if cb.Rule.Scope == ScopeRegistered {
			for j, p := range f.C.Params {
				if t := p.Type; t.Kind == TypePointer && t.LenExpr == "" && t.Elem.Kind == TypeNamed && t.Elem.Name == cb.Rule.Owner {
					u.ownerAddr = goParam[j].Name + ".p"
					break
				}
			}
		}
		if !cb.Token {
			lit, err := e.cbKeyOf(f, cb, goParam)
			if err != nil {
				e.fail(err)
				continue
			}
			e.p("\t%s := %s\n", u.key, lit)
		}
		out = append(out, u)
	}
	return out
}

// fail records the first error of the emitter.
func (e *goEmitter) fail(err error) {
	if e.err == nil {
		e.err = err
	}
}

// cbKeyOf is the Go expression of the handle key of cb in f: cbKey<n> of
// the fields of the key source, a value parameter.
func (e *goEmitter) cbKeyOf(f *BoundFunc, cb *BoundCallback, goParam map[int]GoParam) (string, error) {
	k := e.b.k
	j, why := k.keySource(cb.Decl, cb.Rule, f.C)
	gp, ok := goParam[j]
	if why != "" || !ok || gp.Class != ClassValue {
		return "", fmt.Errorf("bindc: %s: the key source %s of %s is not a value parameter", f.C.Name, cb.Rule.Key.Source, cb.Decl.Name)
	}
	s := f.C.Params[j].Type.Elem.Name
	var fs []string
	for i, ki := range cbKeyFields(cb) {
		rt := e.rawType(cb.Decl.Params[ki].Type)
		x := gp.Name + "." + e.b.FieldNames[s][i]
		if e.b.GoType(e.b.k.types[s].Fields[i].Type) != rt {
			x = rt + "(" + x + ")"
		}
		fs = append(fs, x)
	}
	return cbKeyType(cb) + "{" + strings.Join(fs, ", ") + "}", nil
}

// cbParamDoc documents the callback parameter p of the typed function of c.
func (e *goEmitter) cbParamDoc(c *Function, p GoParam) {
	cb := e.b.k.cbs[c.Params[p.C].Type.Name]
	r := cb.Rule
	if r.Scope == ScopeCall {
		e.p("// C may call %s only during this call; a nil %s passes NULL.\n", p.Name, p.Name)
		return
	}
	if cb.Token {
		e.p("// %s stays registered until the destroy of its %s (its token key\n// does not say which registration a replacement or a clear ends).\n", p.Name, r.Owner)
		return
	}
	e.p("// A nil %s clears it. %s stays registered until a replacement, a\n// clear, ", p.Name, p.Name)
	if len(r.Ends) > 0 {
		e.p("a successful %s of its key, ", strings.Join(r.Ends, " or "))
	}
	e.p("or the destroy of its %s.\n", r.Owner)
}

// cbBefore emits the registry entry of u before the C call: its C pointer
// _c<i>, its token _k<i>, and for a replacement the entry it replaced
// (_s<i>, _h<i>), which a failed call puts back when status is true.
func (e *goEmitter) cbBefore(u cbUse, status bool) {
	cb, n, i := u.cb, u.cb.Index, strconv.Itoa(u.i)
	entry := fmt.Sprintf("cbEntry[%s]{fn: %s", cb.GoName, u.fn)
	if u.ownerAddr != "" {
		entry += ", owner: " + u.ownerAddr
	}
	entry += "}"
	m := "cbMap" + strconv.Itoa(n)
	switch {
	case cb.Token:
		// A token is new for each call: put it, and remove it after the
		// call (call scope) or when the call fails (registered).
		e.p("\tvar _c%s, %s uintptr\n\tif %s != nil {\n\t\t_c%s, %s = cbPtr[%d], cbToken()\n\t\tcbPut(%s, %s, %s)\n", i, u.key, u.fn, i, u.key, n, m, u.key, entry)
		if cb.Rule.Scope == ScopeCall {
			e.p("\t\tdefer cbDrop(%s, %s)\n", m, u.key)
		}
		e.p("\t}\n")
	case cb.Rule.Scope == ScopeCall:
		// A handle key may be live already (a nested call): put the entry
		// back after the call.
		e.p("\tvar _c%s uintptr\n\tif %s != nil {\n\t\t_c%s = cbPtr[%d]\n\t\t_s%s, _h%s := cbPut(%s, %s, %s)\n\t\tdefer cbUndo(%s, %s, _s%s, _h%s)\n\t}\n",
			i, u.fn, i, n, i, i, m, u.key, entry, m, u.key, i, i)
	case status:
		e.p("\tvar _c%s uintptr\n\tvar _s%s cbEntry[%s]\n\tvar _h%s bool\n\tif %s != nil {\n\t\t_c%s = cbPtr[%d]\n\t\t_s%s, _h%s = cbPut(%s, %s, %s)\n\t}\n",
			i, i, cb.GoName, i, u.fn, i, n, i, i, m, u.key, entry)
	default:
		e.p("\tvar _c%s uintptr\n\tif %s != nil {\n\t\t_c%s = cbPtr[%d]\n\t\tcbPut(%s, %s, %s)\n\t}\n", i, u.fn, i, n, m, u.key, entry)
	}
}

// cbFailed emits what a failed C call undoes for u: a registered entry
// goes back to what it replaced (a token's entry is removed).
func (e *goEmitter) cbFailed(u cbUse) {
	if u.cb.Rule.Scope != ScopeRegistered {
		return
	}
	m, i := "cbMap"+strconv.Itoa(u.cb.Index), strconv.Itoa(u.i)
	if u.cb.Token {
		e.p("\t\tif %s != nil {\n\t\t\tcbDrop(%s, %s)\n\t\t}\n", u.fn, m, u.key)
		return
	}
	e.p("\t\tif %s != nil {\n\t\t\tcbUndo(%s, %s, _s%s, _h%s)\n\t\t}\n", u.fn, m, u.key, i, i)
}

// cbSucceeded emits what a successful C call does for u: a clear (a nil
// func) of a registered handle key removes its entry. A token key does not
// say which registration a clear ends; its entries live until their
// owner's destroy.
func (e *goEmitter) cbSucceeded(u cbUse) {
	if u.cb.Rule.Scope != ScopeRegistered || u.cb.Token {
		return
	}
	e.p("\tif %s == nil {\n\t\tcbDrop(cbMap%d, %s)\n\t}\n", u.fn, u.cb.Index, u.key)
}

// cbEnds emits, for the typed function f that is an end function of a
// registered callback with a handle key, the removal of the entry of its
// key after a successful return.
func (e *goEmitter) cbEnds(f *BoundFunc, goParam map[int]GoParam) {
	for _, cb := range e.boundCbs() {
		if cb.Rule.Scope != ScopeRegistered || cb.Token || !slices.Contains(cb.Rule.Ends, f.C.Name) {
			continue
		}
		lit, err := e.cbKeyOf(f, cb, goParam)
		if err != nil {
			e.fail(err)
			continue
		}
		e.p("\tcbDrop(cbMap%d, %s)\n", cb.Index, lit)
	}
}

// cbOwnerDestroyed emits, for the destroy function of the handle type h,
// the removal of every entry whose owner is the handle at addr.
func (e *goEmitter) cbOwnerDestroyed(h, addr string) {
	for _, cb := range e.boundCbs() {
		if cb.Rule.Scope == ScopeRegistered && cb.Rule.Owner == h {
			e.p("\tcbDropOwner(cbMap%d, %s)\n", cb.Index, addr)
		}
	}
}
