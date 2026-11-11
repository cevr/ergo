package std_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	v1 "ergo.local/prototype/runtime/v1"
)

// clauseParam is one function or resource parameter of a std declaration
// whose body is Go: what the signature declares the callee does with it.
type clauseParam struct {
	key    string // std.ArgClauses' Func.Param
	clause string
	target string // the Go target of an extern; "" for an operation
}

// declaredClauses walks the std surface for every parameter whose body is
// Go and so not checked: each extern's, and each operation's of a std
// operation type (its provider is a Go body or an Ergo body of a user,
// which the checker checks; the clause is the type's promise to callers
// either way). A parameter written as a function type has the clause
// "escaping" or "non-escaping" (types.md, Function values 2); one whose
// type holds a resource (resourceParam) "takes" or "lent" (resources R16,
// R17). A function type in any other position (an element, a field, a
// named function type such as Seq) is escaping by rule 2 and is not
// listed: the checker treats a value there as kept.
func declaredClauses(t *testing.T) []clauseParam {
	t.Helper()
	pkgs, _ := stdSurface(t)
	var out []clauseParam
	add := func(name string, sig *check.Signature, target string) {
		for i, p := range sig.Params {
			var clause string
			switch {
			case isFuncParam(p.Type):
				clause = "escaping"
				if p.Type.(*check.Signature).NonEscaping {
					clause = "non-escaping"
				}
			case resourceParam(p.Type, map[check.Type]bool{}):
				clause = "lent"
				if slices.Contains(sig.Takes, i) {
					clause = "takes"
				}
			default:
				continue
			}
			out = append(out, clauseParam{key: name + "." + p.Name, clause: clause, target: target})
		}
	}
	for _, p := range pkgs {
		for _, f := range p.Funcs {
			if f.Decl == nil || f.Decl.Extern == nil || f.Sig == nil {
				continue
			}
			path, name := f.Decl.Extern.Split()
			add(p.Name+"."+f.Name, f.Sig, path+"."+name)
		}
		for _, tn := range p.Types {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			it, ok := n.Underlying().(*check.Interface)
			if !ok {
				continue
			}
			for _, m := range it.Methods {
				if m.Sig != nil {
					add(p.Name+"."+tn.Name+"."+m.Name, m.Sig, "")
				}
			}
		}
	}
	return out
}

// isFuncParam reports a parameter written as a function type: a Signature
// that no name declares.
func isFuncParam(t check.Type) bool {
	_, ok := t.(*check.Signature)
	return ok
}

// resourceParam is this test's own reading of resources R1 for a concrete
// type: a type with a Close method of no parameter and no result, or an
// operation type with such a Close operation, or a slice, Map, tuple,
// record or union that holds one. A type parameter is plain data here:
// a generic body's copies are v1.MovesOnly's (resources R44).
func resourceParam(t check.Type, seen map[check.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t := t.(type) {
	case *check.Named:
		if t.Obj != nil && t.Obj.Pkg != nil {
			for _, f := range t.Obj.Pkg.Funcs {
				if f.Name == "Close" && f.Sig != nil && len(f.Sig.Params) == 1 && len(f.Sig.Results) == 0 && namedOf(f.Sig.Params[0].Type) == t.Obj {
					return true
				}
			}
		}
		if it, ok := t.Underlying().(*check.Interface); ok {
			for _, m := range it.Methods {
				if m.Name == "Close" && m.Sig != nil && len(m.Sig.Params) == 0 && len(m.Sig.Results) == 0 {
					return true
				}
			}
			return false
		}
		return resourceParam(t.Underlying(), seen)
	case *check.Slice:
		return resourceParam(t.Elem, seen)
	case *check.Map:
		return resourceParam(t.Key, seen) || resourceParam(t.Elem, seen)
	case *check.Tuple:
		return slices.ContainsFunc(t.Types, func(e check.Type) bool { return resourceParam(e, seen) })
	case *check.Struct:
		return slices.ContainsFunc(t.Fields, func(f *check.Field) bool { return resourceParam(f.Type, seen) })
	case *check.Union:
		return slices.ContainsFunc(t.Variants, func(v check.Type) bool { return resourceParam(v, seen) })
	}
	return false
}

// namedOf is the type name of t, or of the type a borrow *T points at.
func namedOf(t check.Type) *check.TypeName {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	if n, ok := t.(*check.Named); ok {
		return n.Obj
	}
	return nil
}

// TestExternArgumentClauses is the inventory of ESC1 (lang audit B, F1):
// the checker trusts what a Go body does with a function or resource
// argument, so each such parameter of std is listed in std.ArgClauses with
// the clause its signature declares and the reviewed reason, and a new one
// cannot be added unexamined. A clause that trusts the body (non-escaping:
// runs only before the call returns, one run at a time, never at once with
// its caller; lent: used only before the call returns) needs the review;
// escaping and takes trust nothing. A Go standard library binding's
// non-escaping callback must be in the reviewed v1.GoSyncCallbacks
// (interop.md, Callbacks 1) and is not listed again.
func TestExternArgumentClauses(t *testing.T) {
	listed := map[string]std.ArgClause{}
	for _, c := range std.ArgClauses {
		key := c.Func + "." + c.Param
		if _, dup := listed[key]; dup {
			t.Errorf("std.ArgClauses lists %s twice", key)
		}
		if strings.TrimSpace(c.Reason) == "" {
			t.Errorf("std.ArgClauses: %s has no reason", key)
		}
		listed[key] = c
	}
	seen := map[string]bool{}
	for _, p := range declaredClauses(t) {
		if p.target != "" && !strings.HasPrefix(p.target, v1Path+".") {
			// A Go standard library binding (gobind): its callback clause
			// is v1.GoSyncCallbacks'.
			if p.clause == "non-escaping" && v1.GoSyncCallbacks[p.target] == "" {
				t.Errorf("%s: the binding of %s writes a non-escaping callback, but v1.GoSyncCallbacks does not list %s as calling it only during the call; write escaping, or review it into the table", p.key, p.target, p.target)
			}
			continue
		}
		seen[p.key] = true
		c, ok := listed[p.key]
		switch {
		case !ok:
			t.Errorf("%s is %s and is not in std.ArgClauses: review what its Go body does with it (types.md, Function values 2; resources R17) and list it with the reason", p.key, p.clause)
		case c.Clause != p.clause:
			t.Errorf("%s: std.ArgClauses reviewed it as %s, but its signature declares %s", p.key, c.Clause, p.clause)
		}
	}
	for key := range listed {
		if !seen[key] {
			t.Errorf("std.ArgClauses lists %s, which no std signature declares", key)
		}
	}
	if t.Failed() {
		var all []string
		for _, p := range declaredClauses(t) {
			if p.target == "" || strings.HasPrefix(p.target, v1Path+".") {
				all = append(all, fmt.Sprintf("%s %s", p.key, p.clause))
			}
		}
		t.Logf("declared:\n%s", strings.Join(all, "\n"))
	}
}
