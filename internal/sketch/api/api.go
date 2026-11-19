// Package api projects public contracts from a checked package. It does not
// infer ownership: takes and the own marks are facts of the checker, not of
// docs. The projection is the producer of std code's Contract rows
// (cmd/ergo writes them into the tables); their text, their diff and the
// diff's text are std code's ApiText, ApiDiff and ApiDiffText (SEMTOOLS
// SEM2), and Function and Change stay as the shape of `ergo inspect api
// --json`.
package api

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Function is an exported function, including functions reached as methods.
// Takes contains parameter names (or "self" for a bare operation takes).
// Own names the type parameters written [T own], which a resource may bind
// (types.md, Generics 9).
type Function struct {
	Name            string
	Signature       string
	TypeParameters  []string
	Fails           string
	Needs           string
	Takes           []string
	TakesParameters []int
	TakesSelf       bool
	TakesInferred   bool
	Own             []string
}

func exported(s string) bool { r, _ := utf8.DecodeRuneInString(s); return unicode.IsUpper(r) }

// Functions returns declarations in stable name order. Same-named methods
// are qualified by their receiver type, not their parameter's local name.
func Functions(p *check.Package) []Function {
	out := []Function{}
	for _, f := range p.Funcs {
		if !exported(f.Name) {
			continue
		}
		s := f.Sig
		name := f.Name
		if len(s.Params) > 0 {
			t := s.Params[0].Type
			if ptr, ok := t.(*check.Pointer); ok {
				t = ptr.Elem
			}
			if n, ok := t.(*check.Named); ok && n.Obj.Pkg == p {
				name = n.Obj.Name + "." + name
			}
		}
		v := Function{Name: name, Signature: s.String(), Fails: s.Errors.String(), Needs: s.Uses.String(), Takes: []string{}, TakesParameters: []int{}, TakesSelf: s.TakesSelf, Own: []string{}, TypeParameters: []string{}}
		for _, t := range s.TParams {
			v.TypeParameters = append(v.TypeParameters, t.Name)
			if t.Own {
				v.Own = append(v.Own, t.Name)
			}
		}
		takes := s.Takes
		// Close's receiver take is inserted by the checker (R1), not
		// written in source. Do not label that fact a written clause.
		v.TakesInferred = len(takes) > 0 && f.Decl != nil && !syntax.WritesTakes(f.Decl.Type)
		if len(takes) == 0 && !s.TakesSelf {
			takes = p.Info.Takes[f]
			v.TakesInferred = len(takes) > 0
		}
		if s.TakesSelf {
			v.Takes = append(v.Takes, "self")
		}
		for _, i := range takes {
			v.Takes = append(v.Takes, s.Params[i].Name)
			v.TakesParameters = append(v.TakesParameters, i)
		}
		slices.Sort(v.Takes)
		slices.Sort(v.TakesParameters)
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Function) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Change is an API change, with both contracts retained for JSON clients
// (the JSON of std code's ApiChange, read back by ergo inspect api).
// Adding an own mark excludes no caller; dropping one breaks the callers
// that bind a resource to it.
type Change struct {
	Name                  string
	Old                   *Function
	New                   *Function
	BreaksResourceCallers bool
}
