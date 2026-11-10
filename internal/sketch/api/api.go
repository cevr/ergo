// Package api projects public contracts from a checked package. It does not
// infer ownership: takes and copies are facts of the checker, not of docs.
package api

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/check"
)

// Copy is a type parameter's first checked copy site. An empty Where means
// the body is foreign; it is not an assertion that the parameter moves only.
type Copy struct {
	ParameterIndex int
	Parameter      string
	Where          string
	What           string
	Key            bool
}

// Function is an exported function, including functions reached as methods.
// Takes contains parameter names (or "self" for a bare operation takes).
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
	Copies          []Copy
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
		v := Function{Name: name, Signature: s.String(), Fails: s.Errors.String(), Needs: s.Uses.String(), Takes: []string{}, TakesParameters: []int{}, TakesSelf: s.TakesSelf, Copies: []Copy{}, TypeParameters: []string{}}
		for _, t := range s.TParams {
			v.TypeParameters = append(v.TypeParameters, t.Name)
		}
		takes := s.Takes
		// Close's receiver take is inserted by the checker (R1), not
		// written in source. Do not label that fact a written clause.
		v.TakesInferred = len(takes) > 0 && f.Decl != nil && f.Decl.Type.Takes == nil
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
		for _, c := range p.Info.Copies[f] {
			v.Copies = append(v.Copies, Copy{ParameterIndex: c.TParam, Parameter: s.TParams[c.TParam].Name, Where: c.Where, What: c.What, Key: c.Key})
		}
		slices.SortFunc(v.Copies, func(a, b Copy) int { return strings.Compare(a.Parameter, b.Parameter) })
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Function) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Text is the docs view until a separate doc generator exists.
func Text(fs []Function) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "%s %s", f.Name, f.Signature)
		if len(f.TypeParameters) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(f.TypeParameters, ", "))
		}
		takes := "nothing"
		if len(f.Takes) > 0 {
			takes = strings.Join(f.Takes, ", ")
		}
		origin := "written"
		if f.TakesInferred {
			origin = "inferred"
		}
		fmt.Fprintf(&b, "\n  takes %s (%s)\n", takes, origin)
		for _, c := range f.Copies {
			where := c.Where
			if where == "" {
				where = "foreign body"
			}
			fmt.Fprintf(&b, "  copies %s at %s (plain data only): %s\n", c.Parameter, where, c.What)
		}
	}
	if len(fs) == 0 {
		b.WriteString("no exported function\n")
	}
	return b.String()
}

// Change is an API change, with both contracts retained for JSON clients.
// Moving a copy site changes docs but does not newly exclude resources.
type Change struct {
	Name                  string
	Old                   *Function
	New                   *Function
	BreaksResourceCallers bool
}

// Diff compares checked summaries. A new taking parameter or a newly
// copying type parameter changes what an existing resource caller may do.
func Diff(old, next []Function) []Change {
	before, after := map[string]Function{}, map[string]Function{}
	for _, f := range old {
		before[f.Name] = f
	}
	for _, f := range next {
		after[f.Name] = f
	}
	names := map[string]bool{}
	for n := range before {
		names[n] = true
	}
	for n := range after {
		names[n] = true
	}
	out := []Change{}
	for n := range names {
		a, had := before[n]
		b, has := after[n]
		if had && has && reflect.DeepEqual(a, b) {
			continue
		}
		c := Change{Name: n}
		if had {
			c.Old = &a
		}
		if has {
			c.New = &b
		}
		if had && has {
			if b.TakesSelf && !a.TakesSelf {
				c.BreaksResourceCallers = true
			}
			for _, t := range b.TakesParameters {
				if !slices.Contains(a.TakesParameters, t) {
					c.BreaksResourceCallers = true
				}
			}
			for _, cp := range b.Copies {
				found := slices.ContainsFunc(a.Copies, func(old Copy) bool { return old.ParameterIndex == cp.ParameterIndex })
				if !found {
					c.BreaksResourceCallers = true
				}
			}
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Change) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// DiffText renders changed declarations, not unchanged implementation text.
func DiffText(changes []Change) string {
	var b strings.Builder
	for _, c := range changes {
		fmt.Fprintf(&b, "%s: API change", c.Name)
		if c.BreaksResourceCallers {
			b.WriteString("; breaks callers that pass a resource")
		}
		b.WriteByte('\n')
		if c.Old != nil {
			fmt.Fprintf(&b, "- %s", Text([]Function{*c.Old}))
		}
		if c.New != nil {
			fmt.Fprintf(&b, "+ %s", Text([]Function{*c.New}))
		}
	}
	if len(changes) == 0 {
		b.WriteString("no API change\n")
	}
	return b.String()
}
