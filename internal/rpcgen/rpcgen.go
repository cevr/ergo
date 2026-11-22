// Package rpcgen writes the Ergo source that serves an operation type over
// Effect RPC and provides it over the wire (`ergo gen rpc`; docs/spec/
// rpc.md; R1). The output is ordinary Ergo over std rpc and std json, in
// the package <pkg>rpc beside the contract's package:
//
//   - wire.ergo: Request, a union with a variant per operation whose fields
//     are the operation's parameters, and the int range walkers;
//   - server.ergo: Handle, which decodes a Request, matches it
//     exhaustively and calls the operation on the provider the caller
//     binds, answering a typed failure as its tagged JSON;
//   - client.ergo: Client, a provider of the operation type whose
//     functions call the server, and beside each operation <Op>Checked,
//     the same call with the wire's failures (rpc.Unrepresentable,
//     rpc.Transport) in its row; the provider's function is the checked
//     one with those failures raised as defects.
//
// Each file carries the contract's digest, and `ergo gen rpc --check`
// regenerates and compares. No compiler special case: like `ergo bind
// go`, a generator keeps the language small and the glue inspectable.
package rpcgen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// Files is the generated package: its folder name, relative to the
// contract's package folder, and its files by name.
type Files struct {
	Dir   string
	Files map[string]string
}

// FieldName is the Request field of an operation parameter: the name with
// its first letter raised, so its wire key (check.WireFieldName) is the
// parameter's name, as Effect's payload struct names it, for a parameter
// whose second letter is lower case or a digit (id, userID); a parameter
// such as xID has the key xid, on both ends, since both call WireFieldName.
func FieldName(param string) string {
	if param == "" || param[0] < 'a' || param[0] > 'z' {
		return param
	}
	return string(param[0]-'a'+'A') + param[1:]
}

// Op is one operation of a contract, as the generator reads it.
type Op struct {
	Name   string
	Params []*check.Var
	// Result is the operation's value type; nil for none (unit).
	Result check.Type
	// Fails are the failure types of the row, in its order.
	Fails []check.Type
}

// Ops reads the operations of the operation type opName of pkg, refusing
// what the wire cannot carry.
func Ops(pkg *check.Package, opName string) ([]Op, error) {
	var tn *check.TypeName
	for _, t := range pkg.Types {
		if t.Name == opName {
			tn = t
		}
	}
	if tn == nil {
		return nil, fmt.Errorf("package %s declares no type %s", pkg.Path, opName)
	}
	n, ok := tn.Type.(*check.Named)
	if !ok {
		return nil, fmt.Errorf("%s is not an operation type", opName)
	}
	iface, ok := n.Underlying().(*check.Interface)
	if !ok || len(iface.Methods) == 0 {
		return nil, fmt.Errorf("%s is not an operation type with operations", opName)
	}
	if len(n.TParams) > 0 {
		return nil, fmt.Errorf("%s is generic; an RPC contract names its types", opName)
	}
	var ops []Op
	for _, m := range iface.Methods {
		if !exported(m.Name) {
			return nil, fmt.Errorf("%s.%s is unexported; an RPC operation is exported", opName, m.Name)
		}
		op := Op{Name: m.Name}
		for i, p := range m.Sig.Params {
			if p.Name == "" || p.Name == "_" || !(p.Name[0] >= 'a' && p.Name[0] <= 'z') {
				return nil, fmt.Errorf("%s.%s: parameter %d needs a name that starts with a lower-case letter; it is the payload's key", opName, m.Name, i+1)
			}
			op.Params = append(op.Params, p)
		}
		switch len(m.Sig.Results) {
		case 0:
		case 1:
			op.Result = m.Sig.Results[0]
		default:
			return nil, fmt.Errorf("%s.%s has %d results; an RPC operation has one", opName, m.Name, len(m.Sig.Results))
		}
		if !m.Sig.Uses.Empty() {
			return nil, fmt.Errorf("%s.%s needs %s; an RPC operation needs nothing", opName, m.Name, m.Sig.Uses)
		}
		op.Fails = m.Sig.Errors.Elems()
		for _, p := range op.Params {
			if err := wireKeys(p.Type, false, map[check.Type]bool{}); err != nil {
				return nil, err
			}
		}
		if op.Result != nil {
			if err := wireKeys(op.Result, false, map[check.Type]bool{}); err != nil {
				return nil, err
			}
		}
		for _, f := range op.Fails {
			if err := wireKeys(f, true, map[check.Type]bool{}); err != nil {
				return nil, err
			}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// wireKeys checks the core key rule also at the RPC profile's tagged failure
// boundary. A failure record gets its tag from RPC rather than a core union.
func wireKeys(t check.Type, tagged bool, seen map[check.Type]bool) error {
	u := t
	if n, ok := t.(*check.Named); ok {
		u = n.Underlying()
	}
	if s, ok := u.(*check.Struct); ok && pairOf(t) == nil {
		if why := check.WireRecordKeyReason(s, tagged); why != "" {
			return fmt.Errorf("%s %s", t, why)
		}
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	_, union := u.(*check.Union)
	for _, part := range parts(t) {
		if err := wireKeys(part, union, seen); err != nil {
			return err
		}
	}
	return nil
}

func exported(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }

// Contract reads the operation type opName of pkg, whose import path is
// path, as every generator does: its operations, refusing what the wire
// cannot carry, and the line that names the contract and its digest,
// which each generated file carries.
func Contract(pkg *check.Package, path, opName string) ([]Op, string, error) {
	g, ops, err := readContract(pkg, path, opName)
	if err != nil {
		return nil, "", err
	}
	return ops, g.digestLine(ops), nil
}

func readContract(pkg *check.Package, path, opName string) (*gen, []Op, error) {
	ops, err := Ops(pkg, opName)
	if err != nil {
		return nil, nil, err
	}
	g := &gen{pkg: pkg, path: path, op: opName, imports: map[string]string{"rpc": "rpc"}, wide: map[string]bool{}, walkers: map[string]*walker{}, describe: map[string]string{}}
	g.importOf(pkg)
	for _, op := range ops {
		g.reach(op)
	}
	if g.err == nil {
		g.err = g.checkNames(ops)
	}
	if g.err != nil {
		return nil, nil, g.err
	}
	return g, ops, nil
}

func (g *gen) digestLine(ops []Op) string {
	return fmt.Sprintf("// Contract %s.%s, digest sha256:%s.", g.path, g.op, g.digest(ops))
}

// Generate writes the rpc package of the operation type opName of pkg,
// whose import path is path, over its hub (ergo gen api) at the import
// path apiPath: its Request is the hub's, and Handle answers through the
// hub's Call. The files name the contract by that path, never by how a
// command spelled the folder, so any spelling checks them.
func Generate(pkg *check.Package, path, opName, apiPath string) (Files, error) {
	g, ops, err := readContract(pkg, path, opName)
	if err != nil {
		return Files{}, err
	}
	if apiPath == "" {
		return Files{}, fmt.Errorf("the rpc package is written over the hub; run ergo gen api first")
	}
	g.api = lastElem(apiPath)
	g.imports[apiPath] = g.api
	g.stdAPI = StdAPIName(g.imports)
	g.imports["api"] = g.stdAPI
	if err := g.checkNames(ops); err != nil {
		return Files{}, err
	}
	head := "// Code generated by ergo gen rpc; DO NOT EDIT.\n" + g.digestLine(ops) + "\n\n"
	pkgName := lastElem(path)
	wire := g.wireFile(pkgName, ops)
	server := g.serverFile(ops)
	client := g.clientFile(ops)
	if g.err != nil {
		return Files{}, g.err
	}
	files := map[string]string{}
	for name, body := range map[string]fileText{"wire.ergo": wire, "server.ergo": server, "client.ergo": client} {
		src := head + body.lead + g.importBlock(body.text) + "\n" + body.text
		out, err := format.Source(src)
		if err != nil {
			return Files{}, fmt.Errorf("rpcgen: %s does not format: %v\n%s", name, err, src)
		}
		files[name] = out
	}
	return Files{Dir: pkgName + "rpc", Files: files}, nil
}

// fileText is a generated file's leading comment and its declarations;
// its imports are the packages the declarations name.
type fileText struct{ lead, text string }

func lastElem(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// walker is the range check of one type: the function that fails
// rpc.Unrepresentable for an int the wire cannot carry exactly.
type walker struct {
	name string
	t    check.Type
	body string
}

type gen struct {
	pkg  *check.Package
	path string
	op   string
	// imports are every package a generated file may name, by path, with
	// the name source uses for it.
	imports map[string]string
	// stdAPI is the name of std api.
	stdAPI string
	// wide holds, by type text, whether a type reaches an int the wire
	// cannot carry exactly.
	wide map[string]bool
	// walkers are the range walkers by type text, in order.
	walkers map[string]*walker
	order   []*walker
	// requests are the walkers of the Request variants, by operation.
	requests map[string]string
	// describe is the contract's canonical text of each named type.
	describe map[string]string
	// api is the name the files use for the hub's package.
	api string
	err error
}

func (g *gen) fail(format string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf(format, args...)
	}
}

// importOf records p's import and is the name source uses for it.
func (g *gen) importOf(p *check.Package) string { return importName(g.imports, g.pkg, g.path, p) }

// importName records p's import in imports and is the name source uses for
// it: "" for a prelude name, which is written unqualified. contract is the
// contract's package, whose import path is path: checked as a root, the
// checker names it main, so source imports it by its path.
func importName(imports map[string]string, contract *check.Package, path string, p *check.Package) string {
	if p == nil || p.Path == stdlib.PreludePath {
		return ""
	}
	if p == contract {
		name := lastElem(path)
		imports[path] = name
		return name
	}
	imports[p.Path] = p.Name
	return p.Name
}

// expr is t written in source of the generated package.
func (g *gen) expr(t check.Type) string {
	switch u := t.(type) {
	case *check.Basic:
		return u.Name
	case *check.Named:
		if a := u.Alias(); a != nil {
			return g.expr(a)
		}
		name := u.Obj.Name
		if q := g.importOf(u.Obj.Pkg); q != "" {
			name = q + "." + name
		}
		if len(u.TArgs) > 0 {
			var args []string
			for _, a := range u.TArgs {
				args = append(args, g.expr(a))
			}
			name += "[" + strings.Join(args, ", ") + "]"
		}
		return name
	case *check.Slice:
		return "[]" + g.expr(u.Elem)
	case *check.Map:
		return "Map[" + g.expr(u.Key) + ", " + g.expr(u.Elem) + "]"
	case *check.Tuple:
		var parts []string
		for _, e := range u.Types {
			parts = append(parts, g.expr(e))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	g.fail("the type %s has no wire form an RPC contract can carry", t)
	return "?"
}

// reach walks the types of op: it records their descriptions for the
// digest, their imports and which of them reach a wide int.
func (g *gen) reach(op Op) {
	for _, p := range op.Params {
		g.visit(p.Type)
	}
	if op.Result != nil {
		g.visit(op.Result)
	}
	for _, f := range op.Fails {
		g.visit(f)
		if len(failTags(f)) == 0 {
			g.fail("the failure %s of %s.%s is not a record or a union of records", f, g.op, op.Name)
		}
	}
}

// pairOf is the Wire function of t's by-name pair (types.md, Codecs 1),
// or nil.
func pairOf(t check.Type) *check.Func {
	n, ok := t.(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || len(n.TArgs) > 0 {
		return nil
	}
	for _, f := range n.Obj.Pkg.Funcs {
		if (f.Name == "Wire" || f.Name == "wire") && f.Sig != nil && len(f.Sig.Params) == 1 && len(f.Sig.Results) == 1 && f.Sig.Params[0].Type.String() == t.String() {
			return f
		}
	}
	return nil
}

// wideKind is the check an int kind needs: "Exact" for a signed int wider
// than 53 bits, "ExactUint" for such an unsigned one, "" for none.
func wideKind(k check.BasicKind) string {
	switch k {
	case check.Int, check.Int64:
		return "Exact"
	case check.Uint, check.Uint64, check.Uintptr:
		return "ExactUint"
	}
	return ""
}

// underBasic is the basic type t is, or is defined or refined over.
func underBasic(t check.Type) (*check.Basic, bool) {
	switch u := t.(type) {
	case *check.Basic:
		return u, true
	case *check.Named:
		b, ok := u.Underlying().(*check.Basic)
		return b, ok
	}
	return nil, false
}

// parts are the types t's wire form is made of, each with how a walker
// reaches it; a by-name pair is made of its base.
func parts(t check.Type) []check.Type {
	if f := pairOf(t); f != nil {
		return []check.Type{f.Sig.Results[0]}
	}
	under := t
	if n, ok := t.(*check.Named); ok {
		under = n.Underlying()
	}
	var out []check.Type
	switch u := under.(type) {
	case *check.Struct:
		for _, f := range u.Fields {
			out = append(out, f.Type)
		}
	case *check.Union:
		out = append(out, u.Variants...)
	case *check.Slice:
		out = append(out, u.Elem)
	case *check.Map:
		out = append(out, u.Key, u.Elem)
	case *check.Tuple:
		out = append(out, u.Types...)
	}
	return out
}

// visit records t and every type its wire form reaches, and computes
// which reach a wide int (reachability, so a cycle is fine).
func (g *gen) visit(t check.Type) {
	g.expr(t)
	if n, ok := t.(*check.Named); ok {
		g.describeNamed(n)
	}
	if _, ok := g.wide[t.String()]; ok {
		return
	}
	g.wide[t.String()] = false
	if pairOf(t) == nil {
		if b, ok := underBasic(t); ok {
			if wideKind(b.Kind) != "" {
				g.markWide(t)
			}
			return
		}
		under := t
		if n, ok := t.(*check.Named); ok {
			under = n.Underlying()
		}
		switch under.(type) {
		case *check.Struct, *check.Union, *check.Slice, *check.Map, *check.Tuple:
		default:
			g.fail("the type %s has no wire form an RPC contract can carry", t)
			return
		}
	}
	for _, p := range parts(t) {
		g.visit(p)
	}
}

// markWide marks t wide, then every visited type whose parts reach it.
func (g *gen) markWide(t check.Type) { g.wide[t.String()] = true }

// isWide reports whether t reaches a wide int: the closure of markWide
// over parts, computed on demand.
func (g *gen) isWide(t check.Type) bool {
	return g.reachesWide(t, map[string]bool{})
}

func (g *gen) reachesWide(t check.Type, seen map[string]bool) bool {
	k := t.String()
	if g.wide[k] {
		return true
	}
	if seen[k] {
		return false
	}
	seen[k] = true
	for _, p := range parts(t) {
		if g.reachesWide(p, seen) {
			return true
		}
	}
	return false
}

// walkerOf is t's walker, planned once, or nil when t reaches no wide int.
// A recursive type's walker calls itself by name.
func (g *gen) walkerOf(t check.Type) *walker {
	if !g.isWide(t) {
		return nil
	}
	key := t.String()
	if w := g.walkers[key]; w != nil {
		return w
	}
	w := &walker{t: t, name: "exact" + mangle(g.expr(t))}
	g.walkers[key] = w
	g.order = append(g.order, w)
	var b strings.Builder
	// at writes the check of x, of type u, at the wire key key.
	at := func(b *strings.Builder, x string, u check.Type, key string) {
		if c := g.walkerOf(u); c != nil {
			fmt.Fprintf(b, "%s(%s) catch {\ncase rpc.Unrepresentable as u:\nreturn rpc.Under(%s, u)\n}\n", c.name, x, key)
		}
	}
	if f := pairOf(t); f != nil {
		c := g.walkerOf(f.Sig.Results[0])
		fmt.Fprintf(&b, "try %s(%s.%s(v))\n", c.name, g.importOf(f.Pkg), f.Name)
	} else if bk, ok := underBasic(t); ok {
		k := wideKind(bk.Kind)
		conv := "int64"
		if k == "ExactUint" {
			conv = "uint64"
		}
		// A defined or refined int converts through its base: Ergo
		// converts a refined value in one step only to its base.
		x := "v"
		if _, named := t.(*check.Named); named {
			x = bk.String() + "(v)"
		}
		fmt.Fprintf(&b, "try rpc.%s(\"\", %s(%s))\n", k, conv, x)
	} else {
		under := t
		if n, ok := t.(*check.Named); ok {
			under = n.Underlying()
		}
		switch u := under.(type) {
		case *check.Struct:
			for _, f := range u.Fields {
				at(&b, "v."+f.Name, f.Type, quote(check.WireFieldName(f)))
			}
		case *check.Union:
			b.WriteString("match v {\n")
			for _, vt := range u.Variants {
				var inner strings.Builder
				if vn, ok := vt.(*check.Named); ok {
					if fields, ok := vn.Underlying().(*check.Struct); ok {
						for _, f := range fields.Fields {
							at(&inner, "x."+f.Name, f.Type, quote(check.WireFieldName(f)))
						}
					}
				}
				if inner.Len() > 0 {
					fmt.Fprintf(&b, "case %s as x:\n%s", g.expr(vt), inner.String())
				} else {
					fmt.Fprintf(&b, "case %s:\n", g.expr(vt))
				}
			}
			b.WriteString("}\n")
		case *check.Slice:
			c := g.walkerOf(u.Elem)
			fmt.Fprintf(&b, "for i, x in v {\n%s(x) catch {\ncase rpc.Unrepresentable as u:\nreturn rpc.Under(\"\\(i)\", u)\n}\n}\n", c.name)
		case *check.Map:
			c := g.walkerOf(u.Elem)
			fmt.Fprintf(&b, "for k, x in v {\n%s(x) catch {\ncase rpc.Unrepresentable as u:\nreturn rpc.Under(\"\\(k)\", u)\n}\n}\n", c.name)
		case *check.Tuple:
			var names []string
			var inner strings.Builder
			for i, e := range u.Types {
				names = append(names, fmt.Sprintf("x%d", i))
				at(&inner, names[i], e, quote(fmt.Sprint(i)))
			}
			fmt.Fprintf(&b, "let (%s) = v\n%s", strings.Join(names, ", "), inner.String())
		}
	}
	w.body = b.String()
	return w
}

// mangle turns a type's text into an identifier part: []T is SliceT,
// Map[K, V] MapKV, a tuple Tuple..., and each name is raised.
func mangle(s string) string {
	var b strings.Builder
	up := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case strings.HasPrefix(s[i:], "[]"):
			b.WriteString("Slice")
			i++
			up = true
		case c == '(':
			b.WriteString("Tuple")
			up = true
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			if up && c >= 'a' && c <= 'z' {
				c = c - 'a' + 'A'
			}
			b.WriteByte(c)
			up = false
		default:
			up = true
		}
	}
	return b.String()
}

func quote(s string) string { return fmt.Sprintf("%q", s) }

// describeNamed records n's canonical text for the digest.
func (g *gen) describeNamed(n *check.Named) {
	k := n.String()
	if _, ok := g.describe[k]; ok {
		return
	}
	d := n.Underlying().String()
	if s, ok := n.Underlying().(*check.Struct); ok {
		for _, f := range s.Fields {
			if key := check.WireFieldName(f); key != check.WireFieldName(&check.Field{Name: f.Name}) {
				d += fmt.Sprintf(" key %s=%q", f.Name, key)
			}
		}
	}
	if f := pairOf(n); f != nil {
		d = "wire " + f.Sig.Results[0].String()
	}
	if r := check.RefinementOf(n); r != nil {
		d = "refined " + d
	}
	g.describe[k] = d
}

// digest is the contract's digest: its operations and every named type
// they reach, with their definitions, in a fixed order.
func (g *gen) digest(ops []Op) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s.%s\n", g.path, g.op)
	for _, op := range ops {
		fmt.Fprintf(&b, "%s(", op.Name)
		for i, p := range op.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %s", p.Name, p.Type)
		}
		b.WriteString(")")
		if op.Result != nil {
			fmt.Fprintf(&b, " %s", op.Result)
		}
		for i, f := range op.Fails {
			if i == 0 {
				b.WriteString(" fails ")
			} else {
				b.WriteString(" | ")
			}
			b.WriteString(f.String())
		}
		b.WriteString("\n")
	}
	keys := make([]string, 0, len(g.describe))
	for k := range g.describe {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s = %s\n", k, g.describe[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// reserved are the names the generated functions use for their locals:
// a parameter of one of these names would shadow it.
var reserved = sync.OnceValue(func() map[string]bool {
	return map[string]bool{"client": true, "req": true, "r": true, "text": true, "v": true, "f": true, "e": true, "u": true, "p": true, "x": true}
})

// checkNames refuses names the generated source cannot use: a parameter
// that shadows a local or an import, and two failures with one tag.
func (g *gen) checkNames(ops []Op) error {
	for _, op := range ops {
		for _, p := range op.Params {
			if reserved()[p.Name] {
				return fmt.Errorf("%s.%s: the parameter name %s is one the generated code uses; rename it", g.op, op.Name, p.Name)
			}
			for _, name := range g.imports {
				if p.Name == name {
					return fmt.Errorf("%s.%s: the parameter name %s is the package %s the generated code imports; rename it", g.op, op.Name, p.Name, name)
				}
			}
		}
		tags := map[string]bool{}
		for _, f := range op.Fails {
			for _, tag := range failTags(f) {
				if tags[tag] {
					return fmt.Errorf("%s.%s: two failures have the tag %s on the wire", g.op, op.Name, tag)
				}
				tags[tag] = true
			}
		}
	}
	return nil
}

// failTags are the "_tag" values a failure type writes: a record's name,
// or each variant's name of a union; none for any other type.
func failTags(t check.Type) []string {
	n, ok := t.(*check.Named)
	if !ok || n.Alias() != nil {
		return nil
	}
	switch u := n.Underlying().(type) {
	case *check.Union:
		var tags []string
		for _, v := range u.Variants {
			if vn, ok := v.(*check.Named); ok {
				tags = append(tags, vn.Obj.Name)
			}
		}
		return tags
	case *check.Struct:
		return []string{n.Obj.Name}
	}
	return nil
}

// FailTags are the "_tag"s failure type t's wire form can have: a
// record's name, or each variant's name of a union. A transport's reader
// picks the failure by them.
func FailTags(t check.Type) []string { return failTags(t) }

// IsUnion reports a failure type that is a union, whose own encoding has
// its "_tag".
func IsUnion(t check.Type) bool { return isUnion(t) }

// isUnion reports a failure type that is a union: its own encoding has
// "_tag", so it is sent as it is; a record gets its tag from rpc.Tagged.
func isUnion(t check.Type) bool {
	n, ok := t.(*check.Named)
	if !ok {
		return false
	}
	_, ok = n.Underlying().(*check.Union)
	return ok
}

// importBlock imports each package the declarations text names.
func (g *gen) importBlock(text string) string {
	// The code, without its comments, which name packages in prose.
	var code strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			code.WriteString(line + "\n")
		}
	}
	var paths []string
	for p, name := range g.imports {
		if regexp.MustCompile(`(^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(name) + `\.`).MatchString(code.String()) {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		si, sj := !strings.Contains(paths[i], "/"), !strings.Contains(paths[j], "/")
		if si != sj {
			return si // std first
		}
		return paths[i] < paths[j]
	})
	var b strings.Builder
	for _, p := range paths {
		if name := g.imports[p]; name != lastElem(p) {
			fmt.Fprintf(&b, "import %s %s\n", name, p)
		} else {
			fmt.Fprintf(&b, "import %s\n", p)
		}
	}
	return b.String()
}

func requestName(op string) string { return op + "Request" }

// wireFile is wire.ergo: the package comment, Request and the walkers.
func (g *gen) wireFile(pkgName string, ops []Op) fileText {
	lead := fmt.Sprintf("// Package %srpc serves %s.%s over Effect RPC (Handle) and provides\n// it over the wire (Client), through std rpc (docs/spec/rpc.md).\n\n", pkgName, pkgName, g.op)
	var body strings.Builder
	fmt.Fprintf(&body, "// Request is one call of %s.%s: the hub's Request, a variant per\n// operation whose fields are the operation's parameters, so its payload is\n// the variant's JSON. Each variant is named here too.\n", pkgName, g.op)
	fmt.Fprintf(&body, "type Request = %s.Request\n", g.api)
	for _, op := range ops {
		fmt.Fprintf(&body, "\ntype %s = %s.%s\n", requestName(op.Name), g.api, requestName(op.Name))
	}
	g.requests = map[string]string{}
	for _, op := range ops {
		var lines strings.Builder
		for _, p := range op.Params {
			if c := g.walkerOf(p.Type); c != nil {
				fmt.Fprintf(&lines, "%s(v.%s) catch {\ncase rpc.Unrepresentable as u:\nreturn rpc.Under(%s, u)\n}\n", c.name, FieldName(p.Name), quote(p.Name))
			}
		}
		if lines.Len() > 0 {
			name := "exact" + requestName(op.Name)
			g.requests[op.Name] = name
			fmt.Fprintf(&body, "\nfunc %s(v %s) fails rpc.Unrepresentable {\n%s}\n", name, requestName(op.Name), lines.String())
		}
	}
	// The walkers the server and the client need, planned before their
	// text is written.
	for _, op := range ops {
		if op.Result != nil {
			g.walkerOf(op.Result)
		}
		for _, f := range op.Fails {
			g.walkerOf(f)
		}
	}
	for i := 0; i < len(g.order); i++ {
		w := g.order[i]
		fmt.Fprintf(&body, "\nfunc %s(v %s) fails rpc.Unrepresentable {\n%s}\n", w.name, g.expr(w.t), w.body)
	}
	return fileText{lead: lead, text: body.String()}
}

// check writes the range check of x, by the walker name, that raises the
// defect of an Unrepresentable as a value of type res (rpc.Refuse).
// An empty res is a function with no value: the defect needs no return.
func (g *gen) check(b *strings.Builder, name, x, res string) {
	if name == "" {
		return
	}
	if res == "" {
		fmt.Fprintf(b, "%s(%s) catch {\ncase rpc.Unrepresentable as u:\nrpc.Refuse[string](u)\n}\n", name, x)
		return
	}
	fmt.Fprintf(b, "%s(%s) catch {\ncase rpc.Unrepresentable as u:\nreturn rpc.Refuse[%s](u)\n}\n", name, x, res)
}

func (g *gen) walkerName(t check.Type) string {
	if w := g.walkerOf(t); w != nil {
		return w.name
	}
	return ""
}

// serverFile is server.ergo: Handle.
func (g *gen) serverFile(ops []Op) fileText {
	capName := g.importOf(g.pkg) + "." + g.op
	var b strings.Builder
	fmt.Fprintf(&b, "// Handle runs one call of %s on the provider the caller binds,\n// through the hub's Call: tag names the operation and payload is its\n// Request's JSON. Its value is the result's JSON; a typed failure is\n// Failed with the failure's tagged JSON. A request the contract does not\n// hold and an int the wire cannot carry are defects, which the server\n// answers as Die.\n", capName)
	fmt.Fprintf(&b, "func Handle(tag string, payload string) string fails rpc.Failed needs %s {\ntry handle(tag, payload, %s.Call)\n}\n\n", capName, g.api)
	b.WriteString("// handle is Handle with the Request answered by answer.\n")
	fmt.Fprintf(&b, "func handle[R](tag string, payload string, answer func(r Request) %s.Outcome needs R) string fails rpc.Failed needs R {\n", g.api)
	b.WriteString("let req = rpc.Decode[Request](\"request\", rpc.Tagged(tag + \"Request\", payload)) catch {\ncase rpc.Transport as t:\nreturn rpc.Die[string](\"RpcError\", t.Reason)\n}\n")
	// The arguments' int check, for the operations that carry one.
	checked := 0
	for _, op := range ops {
		if g.requests[op.Name] != "" {
			checked++
		}
	}
	if checked > 0 {
		b.WriteString("match req {\n")
		for _, op := range ops {
			if w := g.requests[op.Name]; w != "" {
				fmt.Fprintf(&b, "case %s as r:\n", requestName(op.Name))
				g.check(&b, w, "r", "string")
			}
		}
		if checked < len(ops) {
			b.WriteString("case _:\n")
		}
		b.WriteString("}\n")
	}
	// The one dispatch (Handle's is the hub's Call); then its Outcome on
	// the wire.
	fmt.Fprintf(&b, "match answer(req) {\ncase %s.Done as done:\nmatch done.Reply {\n", g.api)
	for _, op := range ops {
		reply := g.api + "." + op.Name + "Reply"
		if op.Result == nil {
			fmt.Fprintf(&b, "case %s:\n\"null\"\n", reply)
			continue
		}
		fmt.Fprintf(&b, "case %s as reply:\nlet v = reply.Value\n", reply)
		g.check(&b, g.walkerName(op.Result), "v", "string")
		b.WriteString("rpc.Encode(v)\n")
	}
	fmt.Fprintf(&b, "}\ncase %s.Rejected as rejected:\nmatch rejected.Failure {\n", g.api)
	fails := Failures(ops)
	for _, f := range fails {
		fmt.Fprintf(&b, "case %s.%s as failure:\nlet e = failure.E\n", g.api, FailureVariant(ops, f))
		g.check(&b, g.walkerName(f), "e", "string")
		if isUnion(f) {
			b.WriteString("return rpc.Failed{Text: rpc.Encode(e)}\n")
		} else {
			fmt.Fprintf(&b, "return rpc.Failed{Text: rpc.Tagged(%s, rpc.Encode(e))}\n", quote(failTags(f)[0]))
		}
	}
	if len(fails) == 0 {
		fmt.Fprintf(&b, "case %s.NoFailure:\nrpc.Die[string](\"RpcError\", \"the hub answered NoFailure, which no operation fails\")\n", g.api)
	}
	b.WriteString("}\n}\n}\n")

	// The transport: carry, Wire and its law.
	fmt.Fprintf(&b, "\n// carry is c over Effect RPC's wire: the hub's Call on the Client of a\n// server in this process (rpc.Loopback) whose handle answers with\n// answer, so the call crosses the Client's encoding, the server's\n// envelopes and Handle's reading, and its outcome comes back the same way.\nfunc carry(c Request, answer own func(c Request) %s.Outcome) %s.Outcome {\n", g.api, g.api)
	fmt.Fprintf(&b, "let client = NewClient(rpc.Loopback(func(tag string, payload string) string fails rpc.Failed {\ntry handle(tag, payload, answer)\n}))\nwith context { %s: client } { %s.Call(c) }\n}\n", capName, g.api)
	fmt.Fprintf(&b, "\n// Wire is Effect RPC as a transport of the hub's calls.\ntype Wire {} implements %s.Transport[Request, %s.Outcome]\n", g.stdAPI, g.api)
	fmt.Fprintf(&b, "\n// Carry is carry.\nfunc Carry(w Wire, c Request, answer own func(c Request) %s.Outcome) %s.Outcome { carry(c, answer) }\n", g.api, g.api)
	// Fits: the wire's int rule (rpc.md 5) is the only loss Agrees admits.
	fmt.Fprintf(&b, "\n// Fits reports whether r and o hold only ints the wire carries exactly\n// (an int beyond 2^53-1 is Unrepresentable, a defect on either side).\nfunc Fits(r Request, o %s.Outcome) bool {\n", g.api)
	reqChecks := 0
	for _, op := range ops {
		if g.requests[op.Name] != "" {
			reqChecks++
		}
	}
	if reqChecks > 0 {
		b.WriteString("match r {\n")
		for _, op := range ops {
			if w := g.requests[op.Name]; w != "" {
				fmt.Fprintf(&b, "case %s.%s as x:\n%s(x) catch {\ncase rpc.Unrepresentable:\nreturn false\n}\n", g.api, requestName(op.Name), w)
			}
		}
		if reqChecks < len(ops) {
			b.WriteString("case _:\n")
		}
		b.WriteString("}\n")
	}
	fmt.Fprintf(&b, "match o {\ncase %s.Done as done:\nmatch done.Reply {\n", g.api)
	for _, op := range ops {
		reply := g.api + "." + op.Name + "Reply"
		if w := g.resultWalker(op); w != "" {
			fmt.Fprintf(&b, "case %s as reply:\n%s(reply.Value) catch {\ncase rpc.Unrepresentable:\nreturn false\n}\n", reply, w)
		} else {
			fmt.Fprintf(&b, "case %s:\n", reply)
		}
	}
	fmt.Fprintf(&b, "}\ncase %s.Rejected as rejected:\nmatch rejected.Failure {\n", g.api)
	for _, f := range fails {
		variant := g.api + "." + FailureVariant(ops, f)
		if w := g.walkerName(f); w != "" {
			fmt.Fprintf(&b, "case %s as failure:\n%s(failure.E) catch {\ncase rpc.Unrepresentable:\nreturn false\n}\n", variant, w)
		} else {
			fmt.Fprintf(&b, "case %s:\n", variant)
		}
	}
	if len(fails) == 0 {
		fmt.Fprintf(&b, "case %s.NoFailure:\n", g.api)
	}
	b.WriteString("}\n}\ntrue\n}\n")
	fmt.Fprintf(&b, "\n// Agrees: the wire carries every call and every outcome the call can\n// have unchanged, when they hold only ints it carries exactly (Fits).\nlaw Agrees(r Request, g %s.Given) {\nlet o = %s.OutcomeOf(r, g)\n!Fits(r, o) || %s.Agrees(Wire, r, o)\n}\n", g.api, g.api, g.stdAPI)
	return fileText{text: b.String()}
}

// wideOp reports whether op carries an int the wire may refuse: in an
// argument, its value or a failure. Only such an operation's checked
// function names rpc.Unrepresentable.
func (g *gen) wideOp(op Op) bool {
	if g.requests[op.Name] != "" || (op.Result != nil && g.walkerName(op.Result) != "") {
		return true
	}
	for _, f := range op.Fails {
		if g.walkerName(f) != "" {
			return true
		}
	}
	return false
}

// clientFile is client.ergo: Client and, per operation, its checked
// function and its providing function. One path writes the call: the
// checked function, whose row adds the wire's failures to the
// operation's; the providing function has the operation's own signature
// and raises those failures as defects (rpc.Refuse, rpc.Lost).
func (g *gen) clientFile(ops []Op) fileText {
	capName := g.importOf(g.pkg) + "." + g.op
	var b strings.Builder
	fmt.Fprintf(&b, "// Client is %s over the wire: each operation is one call to the\n// server its Conn names. <Op>Checked fails the wire's failures as typed\n// failures: rpc.Unrepresentable for an int the wire cannot carry (an\n// operation that carries one), rpc.Transport for a lost server, a refused\n// call or an answer that is not the contract's. <Op> provides the\n// operation type, whose row cannot name them, so there they are defects.\n", capName)
	fmt.Fprintf(&b, "type Client { conn rpc.Conn } implements %s\n\n", capName)
	fmt.Fprintf(&b, "// NewClient is the %s that conn serves.\n", capName)
	b.WriteString("func NewClient(conn rpc.Conn) Client { Client{conn: conn} }\n")
	for _, op := range ops {
		res := "string"
		if op.Result != nil {
			res = g.expr(op.Result)
		}
		wide := g.wideOp(op)
		var params, fields, args []string
		for _, p := range op.Params {
			params = append(params, p.Name+" "+g.expr(p.Type))
			fields = append(fields, FieldName(p.Name)+": "+p.Name)
			args = append(args, p.Name)
		}
		var fs []string
		for _, f := range op.Fails {
			fs = append(fs, g.expr(f))
		}
		head := func(name string, fails []string) string {
			sig := fmt.Sprintf("func %s(client Client", name)
			if len(params) > 0 {
				sig += ", " + strings.Join(params, ", ")
			}
			sig += ")"
			if op.Result != nil {
				sig += " " + res
			}
			if len(fails) > 0 {
				sig += " fails " + strings.Join(fails, " | ")
			}
			return sig
		}
		checked := op.Name + "Checked"
		wireFails := append([]string{}, fs...)
		if wide {
			wireFails = append(wireFails, "rpc.Unrepresentable")
		}
		wireFails = append(wireFails, "rpc.Transport")

		// The checked function: the call itself.
		fmt.Fprintf(&b, "\n// %s calls %s's %s on the server; the wire's failures are\n// typed failures.\n%s {\n", checked, capName, op.Name, head(checked, wireFails))
		rn := requestName(op.Name)
		if len(op.Params) == 0 {
			fmt.Fprintf(&b, "let p = %s.%s\n", g.api, rn)
		} else {
			fmt.Fprintf(&b, "let p = %s.%s{%s}\n", g.api, rn, strings.Join(fields, ", "))
			if w := g.requests[op.Name]; w != "" {
				fmt.Fprintf(&b, "try %s(p)\n", w)
			}
		}
		lead := "let text = "
		if op.Result == nil {
			lead = "let _ = "
		}
		unnamed := fmt.Sprintf("return rpc.Transport{Reason: \"%s failed with a failure the contract does not name: \\(f.Text)\"}\n", op.Name)
		fmt.Fprintf(&b, "%stry rpc.Call(client.conn, %s, rpc.Encode(p)) catch {\ncase rpc.Failed as f:\n", lead, quote(op.Name))
		if len(op.Fails) == 0 {
			b.WriteString(unnamed)
		} else {
			b.WriteString("match rpc.TagOf(f.Text) {\n")
			for _, f := range op.Fails {
				var cases []string
				for _, tag := range failTags(f) {
					cases = append(cases, quote(tag))
				}
				fmt.Fprintf(&b, "case %s:\n", strings.Join(cases, ", "))
				fmt.Fprintf(&b, "let e = try rpc.Decode[%s](\"failure\", f.Text)\n", g.expr(f))
				if w := g.walkerName(f); w != "" {
					fmt.Fprintf(&b, "try %s(e)\n", w)
				}
				b.WriteString("return e\n")
			}
			fmt.Fprintf(&b, "case _:\n%s}\n", unnamed)
		}
		if !wide {
			// A contract with no int: a server that refuses one is not
			// this contract's.
			fmt.Fprintf(&b, "case rpc.Unrepresentable as u:\nreturn rpc.Transport{Reason: \"%s: the server refused the int \\(u.Value) at \\(u.Path), which the contract does not carry\"}\n", op.Name)
		}
		b.WriteString("}\n")
		if op.Result != nil {
			fmt.Fprintf(&b, "let v = try rpc.Decode[%s](\"value\", text)\n", res)
			if w := g.walkerName(op.Result); w != "" {
				fmt.Fprintf(&b, "try %s(v)\n", w)
			}
			b.WriteString("v\n")
		}
		b.WriteString("}\n")

		// The providing function: the operation's signature.
		fmt.Fprintf(&b, "\n// %s calls %s's %s on the server, as %s does; an int the\n// wire cannot carry and a failure of the wire are defects.\n%s {\n", op.Name, capName, op.Name, checked, head(op.Name, fs))
		ret := "return "
		if op.Result == nil {
			ret = "" // a function with no value returns none
		}
		if len(op.Fails) > 0 {
			b.WriteString("try ")
		}
		fmt.Fprintf(&b, "%s(%s) catch {\n", checked, strings.Join(append([]string{"client"}, args...), ", "))
		if wide {
			fmt.Fprintf(&b, "case rpc.Unrepresentable as u:\n%srpc.Refuse[%s](u)\n", ret, res)
		}
		fmt.Fprintf(&b, "case rpc.Transport as t:\n%srpc.Lost[%s](t)\n}\n}\n", ret, res)
	}
	return fileText{text: b.String()}
}

// resultWalker is the walker of op's value, "" for none or no value.
func (g *gen) resultWalker(op Op) string {
	if op.Result == nil {
		return ""
	}
	return g.walkerName(op.Result)
}
