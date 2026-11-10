package lower

import (
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The text of an error type's value (types.md, Methods 6: it comes from the
// fields). Error() writes the type's name and then the value's text, which a
// generated function per type writes field by field:
//
//   - a record: {name:text name:text}, its fields in declaration order;
//   - a union: its variant's text;
//   - a slice: [text text];
//   - a string: itself; a bool, an integer, a float or a duration: Go's
//     spelling of the value (runtime/v1 Text*);
//   - an operation value: its type's name in angle brackets; a function
//     value: <func>. Its provider or code is not part of the error's data.
//
// No user method takes part: a 196m String or Error function on a field's
// type cannot change the text, as it could through fmt's %v. Every text
// function is of a concrete type: an instance of a generic type gets its
// own, over its substituted fields (lower-v1 stage 4), so a field of type T
// prints as the instance's T. The Error method of a generic error type
// cannot be specialised in Go, so it switches on its instance: one case
// per instance of the type that the program's types name (instancesOf),
// which covers every value that reaches an error report, since a report
// reads a row of a non-generic body, whose types are concrete. A Map or a
// tuple field is refused where the error type is declared (#49). A borrow
// is never a field. Every local of a generated function comes from a namer
// whose parent is the package scope.
//
// Prior art: Gleam's string.inspect and Elixir's Inspect derive a value's
// text from its structure, not from user methods, unless the user opts in.
// Elixir's protocol consolidation makes that dispatch static at compile
// time, as the per-instance functions here are.

// textKey is the package-scope key of the text function of t: one per Go
// type. Its identity is the canonical Go type the function takes (typeKey,
// which erases row arguments as goType does), so a declared type and an
// instance of it that differ only in rows, such as Hold and Hold[Bad] of
// type Hold[E] { F func() fails E }, share one function (196b).
func (l *lowerer) textKey(t check.Type, site syntax.Node) (pkgKey, string) {
	switch u := t.(type) {
	case *check.Named:
		td := l.typeDecl(u.Obj, site)
		return pkgKey{"text", "named " + l.typeKey(u)}, "text" + td.name
	case *check.Slice:
		return pkgKey{"text", "slice " + l.typeKey(t)}, "textList"
	}
	return pkgKey{}, ""
}

// textTypes walks the types whose text functions the text of t needs, in
// a fixed order, calling visit once for each; it refuses, at site, a type
// whose text is not lowered.
func (l *lowerer) textTypes(t check.Type, site syntax.Node, seen map[pkgKey]bool, visit func(check.Type)) {
	switch u := t.(type) {
	case *check.Basic:
		switch {
		case u.Kind == check.String, u.Kind == check.Bool, u.Kind == check.Duration,
			u.Kind >= check.Int && u.Kind <= check.Uintptr, u.Kind == check.Float32, u.Kind == check.Float64:
			return
		}
	case *check.Signature:
		return
	case *check.TypeParam:
		// textRoots passes concrete types, and an instance's fields are
		// substituted (check: Named.Underlying), so no walk meets one.
		ice(site, "the text of a concrete type reaches the type parameter %s", u.Name)
	case *check.Named:
		td := l.typeDecl(u.Obj, site)
		if td.kind == kindOps {
			return
		}
		if len(td.tparams) > 0 && len(u.TArgs) == 0 {
			ice(site, "the text of the generic type %s, which only an instance has", t)
		}
		key, _ := l.textKey(t, site)
		if seen[key] {
			return
		}
		seen[key] = true
		visit(t)
		switch td.kind {
		case kindRecord:
			for _, f := range textFields(u, site) {
				l.textTypes(f.Type, site, seen, visit)
			}
		case kindUnion:
			for _, v := range textVariants(u, site) {
				l.textTypes(v, site, seen, visit)
			}
		case kindDefined:
			// A defined type's text is its base type's.
			l.textTypes(u.Defined(), site, seen, visit)
		}
		return
	case *check.Slice:
		if mentionsTypeParam(u) {
			ice(site, "the text of a concrete type reaches the slice type %s of a type parameter", t)
		}
		key, _ := l.textKey(t, site)
		if seen[key] {
			return
		}
		seen[key] = true
		visit(t)
		l.textTypes(u.Elem, site, seen, visit)
		return
	}
	refuse(site, "the text of an error type with a field of type %s waits for derived text forms (#49)", t)
}

// errorMethod writes the Error method of the error type td, and queues the
// text functions its text needs.
func (l *lowerer) errorMethod(b *strings.Builder, td *typeDecl) {
	site := syntax.Node(td.tn.Decl.Name)
	roots := l.textRoots(td.named)
	for _, r := range roots {
		l.textTypes(r, site, map[pkgKey]bool{}, func(t check.Type) { l.textFunc(t, site) })
	}
	scope := newNamer(l.names)
	recv := scope.alloc("e")
	name := strconv.Quote(td.tn.Name)
	head := "func (" + recv + " " + l.recvType(td) + ") Error() string {\n"
	if len(td.tparams) == 0 {
		key, _ := l.textKey(td.named, site)
		b.WriteString(head + "\treturn " + name + " + " + l.pkgNames[key] + "(" + recv + ")\n}\n\n")
		return
	}
	// A generic error type: one case per instance, by its Go type (two
	// instances that differ in a row only are one Go type). An instance no
	// type of the program names never reaches a report; it prints its
	// type's name.
	v := scope.alloc("v")
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	var cases strings.Builder
	done := map[string]bool{}
	for _, r := range roots {
		goType := tc.goType(r)
		if done[goType] {
			continue
		}
		done[goType] = true
		key, _ := l.textKey(r, site)
		cases.WriteString("\tcase " + goType + ":\n\t\treturn " + name + " + " + l.pkgNames[key] + "(" + v + ")\n")
	}
	if cases.Len() == 0 {
		b.WriteString(head + "\treturn " + name + "\n}\n\n")
		return
	}
	b.WriteString(head + "\tswitch " + v + " := any(" + recv + ").(type) {\n" + cases.String() + "\t}\n\treturn " + name + "\n}\n\n")
}

// textRoots are the concrete types whose text the Error method of the
// error type named writes: named itself, or each instance of a generic one
// (instancesOf). A type generic over rows only is concrete: Go erases rows.
func (l *lowerer) textRoots(named *check.Named) []check.Type {
	for _, tp := range named.TParams {
		if tp.Kind == check.KindType {
			return l.instancesOf(named.Obj)
		}
	}
	return []check.Type{named}
}

// textFields are the fields of the record type u, an instance's
// substituted.
func textFields(u *check.Named, site syntax.Node) []*check.Field {
	s, ok := u.Underlying().(*check.Struct)
	if !ok {
		ice(site, "the record %s has no struct", u)
	}
	return s.Fields
}

// textVariants are the variants of the union type u, an instance's
// substituted.
func textVariants(u *check.Named, site syntax.Node) []check.Type {
	s, ok := u.Underlying().(*check.Union)
	if !ok {
		ice(site, "the union %s has no variants", u)
	}
	return s.Variants
}

// instancesOf is every concrete instance of the generic type tn that a
// type of the program names: in an expression, a call's error row, a
// declaration, a pattern or a literal's signature, of the user package and
// of every library package, and in the fields of those instances. It is
// computed once, sorted by the instances' canonical keys (typeKey), never
// by their display, which instances of two packages' types can share.
func (l *lowerer) instancesOf(tn *check.TypeName) []check.Type {
	if l.instances == nil {
		l.instances = map[*check.TypeName][]check.Type{}
		named := map[string]bool{}
		visited := map[check.Type]bool{}
		var walk func(t check.Type)
		walkRow := func(r *check.Row) {
			if r != nil {
				for _, e := range rowElems(r) {
					walk(e)
				}
			}
		}
		walk = func(t check.Type) {
			if t == nil || visited[t] {
				return
			}
			visited[t] = true
			switch u := t.(type) {
			case *check.Named:
				for _, a := range u.TArgs {
					walk(a)
				}
				if len(u.TArgs) == 0 || mentionsTypeParam(u) {
					return
				}
				if key := l.typeKey(u); !named[key] {
					named[key] = true
					l.instances[u.Obj] = append(l.instances[u.Obj], u)
				}
				switch s := u.Underlying().(type) {
				case *check.Struct:
					for _, f := range s.Fields {
						walk(f.Type)
					}
				case *check.Union:
					for _, v := range s.Variants {
						walk(v)
					}
				}
				walk(u.Defined())
			case *check.Slice:
				walk(u.Elem)
			case *check.Map:
				walk(u.Key)
				walk(u.Elem)
			case *check.Pointer:
				walk(u.Elem)
			case *check.Tuple:
				for _, e := range u.Types {
					walk(e)
				}
			case *check.Union:
				for _, v := range u.Variants {
					walk(v)
				}
			case *check.Signature:
				for _, p := range u.Params {
					walk(p.Type)
				}
				for _, r := range u.Results {
					walk(r)
				}
				walkRow(u.Errors)
			}
		}
		for _, p := range append([]*check.Package{l.pkg}, l.libraryPackages()...) {
			info := p.Info
			if info == nil {
				continue
			}
			for _, t := range info.Types {
				walk(t)
			}
			for _, c := range info.Calls {
				walk(c.Type)
				walkRow(c.Fails)
			}
			for _, o := range info.Defs {
				switch o := o.(type) {
				case *check.Var:
					walk(o.Type)
				case *check.Func:
					walk(o.Sig)
				case *check.Field:
					walk(o.Type)
				}
			}
			for _, pt := range info.Patterns {
				walk(pt.Type)
			}
			for _, s := range info.Literals {
				walk(s)
			}
		}
		for _, list := range l.instances {
			sort.Slice(list, func(i, j int) bool { return l.typeKey(list[i]) < l.typeKey(list[j]) })
		}
	}
	return l.instances[tn]
}

// textExpr is the Go expression of val's text, val of type t, whose text
// function textTypes has queued.
func (l *lowerer) textExpr(t check.Type, val string, site syntax.Node) string {
	switch u := t.(type) {
	case *check.Basic:
		switch {
		case u.Kind == check.String:
			return val
		case u.Kind == check.Bool:
			return l.rt() + ".TextBool(" + val + ")"
		case u.Kind == check.Duration:
			return l.rt() + ".TextDuration(" + val + ")"
		case u.Kind >= check.Int && u.Kind <= check.Int64:
			return l.rt() + ".TextInt(int64(" + val + "))"
		case u.Kind >= check.Uint && u.Kind <= check.Uintptr:
			return l.rt() + ".TextUint(uint64(" + val + "))"
		case u.Kind == check.Float32:
			return l.rt() + ".TextFloat(float64(" + val + "), 32)"
		case u.Kind == check.Float64:
			return l.rt() + ".TextFloat(" + val + ", 64)"
		}
	case *check.Signature:
		return `"<func>"`
	case *check.Named:
		if l.typeDecl(u.Obj, site).kind == kindOps {
			return strconv.Quote("<" + u.Obj.Name + ">")
		}
	}
	key, _ := l.textKey(t, site)
	return l.pkgNames[key] + "(" + val + ")"
}

// textFunc queues the generated text function of t once. Its name was
// given by the naming pass; its type parameters, parameter and locals come
// from its own namer.
func (l *lowerer) textFunc(t check.Type, site syntax.Node) {
	key, base := l.textKey(t, site)
	if l.texts[key] {
		return
	}
	l.texts[key] = true
	name := l.pkgName(key, base)
	scope := newNamer(l.names)
	var head string
	var body func() string
	switch u := t.(type) {
	case *check.Named:
		// t is concrete (textTypes): an instance's text function is its
		// own, over its substituted fields, with no type parameter.
		td := l.useType(l.typeDecl(u.Obj, site))
		tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
		v := scope.alloc("v")
		head = "(" + v + " " + tc.goType(u) + ")"
		switch td.kind {
		case kindRecord:
			body = func() string {
				var parts []string
				for i, f := range textFields(u, site) {
					sep := ""
					if i > 0 {
						sep = " "
					}
					parts = append(parts, strconv.Quote(sep+f.Name+":")+" + "+l.textExpr(f.Type, v+"."+td.fieldName[f.Name], site))
				}
				if len(parts) == 0 {
					return "\treturn \"{}\"\n"
				}
				return "\treturn \"{\" + " + strings.Join(parts, " + ") + " + \"}\"\n"
			}
		case kindUnion:
			if fu := l.flatUnionOf(u); fu != nil && fu.generic {
				// Option (flatunion.go): the text of the variant its tag
				// names.
				body = func() string {
					var s strings.Builder
					s.WriteString("\t" + flatHead(v, len(fu.variants)) + "\n")
					for k, vt := range fu.variants {
						s.WriteString("\tcase " + strconv.Itoa(k) + ":\n\t\treturn " + l.textExpr(vt, fu.flatNarrow(tc, v, k), site) + "\n")
					}
					s.WriteString("\t}\n\treturn \"<nil>\"\n")
					return s.String()
				}
				break
			} else if fu != nil && unionMutant != "sig-bail" {
				flatBail(site, fu) // only an error's text names a union: it keeps its interface (flatunion.go)
			}
			body = func() string {
				x := scope.alloc("x")
				var s strings.Builder
				s.WriteString("\tswitch " + x + " := " + v + ".(type) {\n")
				for _, vt := range textVariants(u, site) {
					s.WriteString("\tcase " + tc.goType(vt) + ":\n\t\treturn " + l.textExpr(vt, x, site) + "\n")
				}
				s.WriteString("\t}\n\treturn \"<nil>\"\n")
				return s.String()
			}
		case kindDefined:
			body = func() string {
				def := u.Defined()
				return "\treturn " + l.textExpr(def, tc.goType(def)+"("+v+")", site) + "\n"
			}
		}
	case *check.Slice:
		goType := (&typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}).goType(t)
		v := scope.alloc("v")
		head = "(" + v + " " + goType + ")"
		body = func() string {
			texts, i, e := scope.alloc("texts"), scope.alloc("i"), scope.alloc("e")
			return "\t" + texts + " := make([]string, len(" + v + "))\n\tfor " + i + ", " + e + " := range " + v + " {\n\t\t" + texts + "[" + i + "] = " + l.textExpr(u.Elem, e, site) + "\n\t}\n\treturn " + l.rt() + ".TextList(" + texts + ")\n"
		}
	}
	l.pending = append(l.pending, func() {
		l.gen.WriteString("func " + name + head + " string {\n" + body() + "}\n\n")
	})
}
