// Package tsproj projects checked wire contracts to Effect RPC schemas.
// It emits declarations for TypeScript's content mapper and JavaScript for
// runtime loaders from one walk. It never lowers Ergo functions to JavaScript.
package tsproj

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

type Options struct {
	JavaScript bool
	Encoding   string // utf-8 or utf-16; empty means utf-8
}

// Output is TypeScript 7.1's MappedOutput. Mapping kinds follow its protocol:
// Verbatim=0, Atom=1, Alias=2. Generated scaffolding has no mapping.
type Output struct {
	Text        string       `json:"text"`
	Extension   string       `json:"extension"`
	Mappings    [][]int      `json:"mappings"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type Diagnostic struct {
	Message string `json:"messageText"`
	Start   int    `json:"start"`
	Length  int    `json:"length"`
	Code    int    `json:"code"`
}

type emitter struct {
	Options
	source  string
	b       strings.Builder
	maps    [][]int
	names   map[check.Type]string
	state   map[check.Type]int
	failure map[check.Type]bool
	local   map[*syntax.TypeDecl]bool
	err     error
}

// Project reads the compiler's checked package, never reparsing a signature.
// Every mapped file exposes its package's operation types and the exported
// types they reach, under their Ergo names. Reachable imported types are embedded under private schema names,
// so their Ergo import paths need no TypeScript path translation.
func Project(pkg *check.Package, file *syntax.File, source string, options Options) (Output, error) {
	if options.Encoding == "" {
		options.Encoding = "utf-8"
	}
	if options.Encoding != "utf-8" && options.Encoding != "utf-16" {
		return Output{}, fmt.Errorf("unsupported position encoding %q", options.Encoding)
	}
	g := &emitter{Options: options, source: source, maps: [][]int{}, names: map[check.Type]string{}, state: map[check.Type]int{}, failure: map[check.Type]bool{}, local: map[*syntax.TypeDecl]bool{}}
	for _, d := range file.Decls {
		if d, ok := d.(*syntax.TypeDecl); ok {
			g.local[d] = true
		}
	}
	var contracts []*check.TypeName
	for _, obj := range pkg.Types {
		if !public(obj.Name) {
			continue
		}
		n, ok := obj.Type.(*check.Named)
		if !ok {
			continue
		}
		if len(n.TParams) > 0 {
			if _, ok := n.Underlying().(*check.Interface); ok {
				return Output{}, fmt.Errorf("%s is generic; an RPC contract names its types", obj.Name)
			}
			continue
		} // instances are reached from contracts
		g.names[n] = obj.Name
		if _, ok := n.Underlying().(*check.Interface); ok {
			contracts = append(contracts, obj)
		}
	}
	// Tags are a property of the wire position, not a TypeScript naming rule.
	for _, c := range contracts {
		ops, err := rpcgen.Ops(pkg, c.Name)
		if err != nil {
			return Output{}, err
		}
		for _, op := range ops {
			for _, f := range op.Fails {
				g.markFailure(f)
			}
		}
	}
	g.s("import { Schema as $Schema } from \"effect\";\nimport { Rpc as $Rpc, RpcGroup as $RpcGroup } from \"effect/rpc\";\n")
	// Only the wire is projected: the types the operation types reach.
	// Other exported types (machines, records no operation carries) are
	// not part of the wire and stay out of the mapped file.
	for _, c := range contracts {
		ops, _ := rpcgen.Ops(pkg, c.Name)
		for _, op := range ops {
			for _, p := range op.Params {
				g.schema(p.Type)
			}
			if op.Result != nil {
				g.schema(op.Result)
			}
			for _, f := range op.Fails {
				g.schema(f)
			}
		}
		g.doc(c)
		g.s("export class ")
		g.name(c.Name, c.Name, c.Pos, g.local[c.Decl])
		g.s(" extends $RpcGroup.make(\n")
		for _, op := range ops {
			g.s("$Rpc.make(" + strconv.Quote(op.Name) + ", { payload: { ")
			for _, p := range op.Params {
				g.s(strconv.Quote(check.WireFieldName(rpcgen.FieldName(p.Name))) + ": " + g.schema(p.Type) + ", ")
			}
			g.s("}, success: ")
			if op.Result == nil {
				g.s("$Schema.Void")
			} else {
				g.s(g.schema(op.Result))
			}
			if len(op.Fails) > 0 {
				g.s(", error: ")
				var fs []string
				for _, f := range op.Fails {
					fs = append(fs, g.errorSchema(f))
				}
				g.s(union(fs))
			}
			g.s(" }),\n")
		}
		g.s(") {}\n")
	}
	if g.err != nil {
		return Output{}, g.err
	}
	ext := ".ts"
	if options.JavaScript {
		ext = ".js"
	}
	return Output{Text: g.b.String(), Extension: ext, Mappings: g.maps}, nil
}

func public(s string) bool    { return s != "" && s[0] >= 'A' && s[0] <= 'Z' }
func (g *emitter) s(s string) { g.b.WriteString(s) }
func units(s, enc string) int {
	if enc == "utf-16" {
		return len(utf16.Encode([]rune(s)))
	}
	return len(s)
}
func (g *emitter) name(text, original string, pos syntax.Pos, local bool) {
	o := int(pos)
	if local && o >= 0 && o+len(original) <= len(g.source) && g.source[o:o+len(original)] == original {
		kind := 0
		if text != original {
			kind = 2
		}
		g.maps = append(g.maps, []int{units(g.b.String(), g.Encoding), units(text, g.Encoding), units(g.source[:o], g.Encoding), units(original, g.Encoding), kind})
	}
	g.s(text)
}
func (g *emitter) fail(t check.Type, why string) string {
	if g.err == nil {
		g.err = fmt.Errorf("TypeScript wire projection of %s: %s", t, why)
	}
	return ""
}
func (g *emitter) markFailure(t check.Type) {
	if n, ok := t.(*check.Named); ok {
		if u, ok := n.Underlying().(*check.Union); ok {
			for _, v := range u.Variants {
				g.markFailure(v)
			}
			return
		}
		g.failure[t] = true
	}
}
func union(xs []string) string {
	if len(xs) == 1 {
		return xs[0]
	}
	return "$Schema.Union([" + strings.Join(xs, ", ") + "])"
}
func (g *emitter) schema(t check.Type) string {
	switch u := t.(type) {
	case *check.Basic:
		switch u.Kind {
		case check.String:
			return "$Schema.String"
		case check.Bool:
			return "$Schema.Boolean"
		case check.Int, check.Int8, check.Int16, check.Int32, check.Int64, check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr, check.Duration:
			return "$Schema.Int"
		case check.Float32, check.Float64:
			return g.fail(t, "non-finite JSON float form needs an explicit schema")
		}
	case *check.Slice:
		return "$Schema.Array(" + g.schema(u.Elem) + ")"
	case *check.Map:
		b, ok := u.Key.(*check.Basic)
		if !ok || b.Kind != check.String {
			return g.fail(t, "only string map keys are supported")
		}
		return "$Schema.Record($Schema.String, " + g.schema(u.Elem) + ")"
	case *check.Tuple:
		var xs []string
		for _, e := range u.Types {
			xs = append(xs, g.schema(e))
		}
		return "$Schema.Tuple([" + strings.Join(xs, ", ") + "])"
	case *check.Named:
		if u.Obj.Pkg != nil && u.Obj.Pkg.Path == std.PreludePath && u.Obj.Name == "Option" && len(u.TArgs) == 1 {
			return "$Schema.Option(" + g.schema(u.TArgs[0]) + ")"
		}
		g.declare(u)
		if g.failure[t] {
			return "$value" + g.names[t]
		}
		return g.names[t]
	}
	return g.fail(t, "unsupported wire shape")
}

// declare topologically emits schemas. Recursive schemas need an explicit
// recursive TS type and are diagnosed until that projection is supported.
func (g *emitter) declare(n *check.Named) {
	if g.state[n] == 2 {
		return
	}
	if g.state[n] == 1 {
		g.fail(n, "recursive schemas are not supported yet")
		return
	}
	g.state[n] = 1
	name := g.names[n]
	if name == "" {
		name = fmt.Sprintf("ergoType%d", len(g.names))
		g.names[n] = name
	}
	if n.Obj.Refine != nil {
		g.fail(n, "refinement predicates are not projected")
		return
	}
	// A by-name codec is not a structural record codec. Refuse it rather
	// than silently constructing a schema for the wrong wire form.
	if n.Obj.Pkg != nil {
		for _, f := range n.Obj.Pkg.Funcs {
			if f.Name == "Wire" && f.Sig != nil && len(f.Sig.Params) == 1 && f.Sig.Params[0].Type == n {
				g.fail(n, "by-name codecs need an explicit wire projection")
				return
			}
		}
	}
	under := n.Underlying()
	if a := n.Alias(); a != nil {
		under = a
	}
	// Resolve dependencies first; schema() can emit a declaration.
	switch u := under.(type) {
	case *check.Struct:
		for _, f := range u.Fields {
			g.schema(f.Type)
		}
	case *check.Union:
		for _, v := range u.Variants {
			g.schema(v)
		}
	default:
		g.schema(under)
	}
	g.doc(n.Obj)
	export := ""
	if g.names[n] == n.Obj.Name {
		export = "export "
	}
	local := g.local[n.Obj.Decl]
	if g.failure[n] {
		if _, ok := under.(*check.Struct); !ok {
			g.fail(n, "failure is not a record")
			return
		}
		g.s(export + "class ")
		g.name(name, n.Obj.Name, n.Obj.Pos, local)
		g.s(" extends $Schema.TaggedError")
		if !g.JavaScript {
			g.s("<" + name + ">")
		}
		g.s("()(" + strconv.Quote(n.Obj.Name) + ", ")
		g.fields(under.(*check.Struct), local)
		g.s(") {}\n")
		g.s("const $value" + name + " = $Schema.Struct(")
		g.fields(under.(*check.Struct), local)
		g.s(");\n")
	} else {
		g.s(export + "const ")
		g.name(name, n.Obj.Name, n.Obj.Pos, local)
		g.s(" = ")
		switch u := under.(type) {
		case *check.Struct:
			g.s("$Schema.Struct(")
			g.fields(u, local)
			g.s(")")
		case *check.Union:
			g.s("$Schema.Union([")
			for _, v := range u.Variants {
				vn, ok := v.(*check.Named)
				if !ok {
					g.fail(v, "union variant is not named")
					continue
				}
				st, ok := vn.Underlying().(*check.Struct)
				if !ok {
					g.fail(v, "union variant is not a record")
					continue
				}
				g.s("$Schema.TaggedStruct(" + strconv.Quote(vn.Obj.Name) + ", ")
				g.fields(st, g.local[vn.Obj.Decl])
				g.s("), ")
			}
			g.s("])")
		default:
			g.s(g.schema(under))
		}
		g.s(";\n")
		if !g.JavaScript {
			g.s(export + "type " + name + " = $Schema.Schema.Type<typeof " + name + ">;\n")
		}
	}
	g.state[n] = 2
}
func (g *emitter) errorSchema(t check.Type) string {
	if n, ok := t.(*check.Named); ok {
		if u, ok := n.Underlying().(*check.Union); ok {
			var xs []string
			for _, v := range u.Variants {
				xs = append(xs, g.errorSchema(v))
			}
			return union(xs)
		}
		return g.names[n]
	}
	return g.fail(t, "failure is not named")
}
func (g *emitter) fields(s *check.Struct, local bool) {
	g.s("{ ")
	for _, f := range s.Fields {
		g.s("\"")
		g.name(check.WireFieldName(f.Name), f.Name, f.Pos, local)
		g.s("\": " + g.schema(f.Type) + ", ")
	}
	g.s("}")
}
func (g *emitter) doc(obj *check.TypeName) {
	if !g.local[obj.Decl] || obj.Decl == nil || obj.Decl.Doc == nil {
		return
	}
	g.s("/**\n")
	for _, c := range obj.Decl.Doc.List {
		g.s(" * " + strings.ReplaceAll(strings.TrimPrefix(c.Text, "//"), "*/", "* /") + "\n")
	}
	g.s(" */\n")
}
