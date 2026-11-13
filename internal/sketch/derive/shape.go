package derive

import (
	"ergo.local/prototype/internal/sketch/regexpplan"
)

// Capture shapes (patterns.md, Capture shapes). Each named capture belongs
// to the scope of the nearest named capture around it, or of the whole
// pattern. Its participation in each match of its scope gives its field:
// exactly once a value, at most once an Option, possibly more a slice.

// Count is a capture's participation in its scope.
type Count uint8

const (
	One  Count = iota // exactly once in every match of its scope
	Opt               // at most once, not in every match
	Many              // possibly more than once
)

// Field is one capture of a scope. Record is nil for a capture with no
// named capture inside it (a string field) and the nested record else.
type Field struct {
	Name   string
	Tag    int // the capture's tag in the plan: its index in pattern order, from 1
	Count  Count
	Record *Record
}

// Record is a scope's fields, in pattern order, and the record type's name.
type Record struct {
	Type   string
	Fields []*Field
}

// Shape is the derived type of a pattern: a record, or a union whose
// variants are the captures of the top-level alternation.
type Shape struct {
	Union    bool
	Record   *Record  // the record form
	Variants []*Field // the union form: one capture per variant
	// Repeated is a capture under a repeat of more than one: the matcher
	// returns events, not fixed slots.
	Repeated bool
}

// span is (min, max) participation with max 2 meaning "more than one".
type span struct{ min, max int }

func capAdd(a, b int) int { return min(a+b, 2) }

// participation is the count of the capture x in the subtree n of its
// scope; it does not enter a named capture other than x (another scope).
func participation(n, x *regexpplan.Node) span {
	switch n.Kind {
	case regexpplan.Capture:
		if n == x {
			return span{1, 1}
		}
		return span{}
	case regexpplan.Concat:
		var s span
		for _, c := range n.Children {
			t := participation(c, x)
			s = span{capAdd(s.min, t.min), capAdd(s.max, t.max)}
		}
		return s
	case regexpplan.Alternative:
		s := span{2, 0}
		for _, c := range n.Children {
			t := participation(c, x)
			s = span{min(s.min, t.min), max(s.max, t.max)}
		}
		return s
	case regexpplan.Repeat:
		t := participation(n.Children[0], x)
		hi := n.Max
		if hi < 0 || hi > 2 {
			hi = 2
		}
		return span{min(t.min*n.Min, 2), min(t.max*hi, 2)}
	}
	return span{}
}

// scopeCaptures lists the named captures directly in the scope n: those
// not inside another named capture of n.
func scopeCaptures(n *regexpplan.Node, out []*regexpplan.Node) []*regexpplan.Node {
	if n.Kind == regexpplan.Capture {
		return append(out, n)
	}
	for _, c := range n.Children {
		out = scopeCaptures(c, out)
	}
	return out
}

// shapeOf derives the shape of a parsed pattern for the type named name,
// or refuses it (Capture shapes 4 and 7).
func shapeOf(name string, pt *parsed) (*Shape, *PatternError) {
	tags := map[*regexpplan.Node]int{}
	var number func(*regexpplan.Node)
	number = func(n *regexpplan.Node) {
		if n.Kind == regexpplan.Capture {
			tags[n] = len(tags) + 1
		}
		for _, c := range n.Children {
			number(c)
		}
	}
	number(pt.root)
	if err := repeatedGroups(pt.root); err != nil {
		return nil, err
	}
	var record func(typ string, scope *regexpplan.Node) *Record
	record = func(typ string, scope *regexpplan.Node) *Record {
		r := &Record{Type: typ}
		for _, c := range scopeCaptures(scopeBody(scope), nil) {
			s := participation(scopeBody(scope), c)
			f := &Field{Name: c.Name, Tag: tags[c]}
			switch {
			case s.max >= 2:
				f.Count = Many
			case s.min == 1:
				f.Count = One
			default:
				f.Count = Opt
			}
			if len(scopeCaptures(c.Children[0], nil)) > 0 {
				f.Record = record(typ+c.Name, c)
			}
			r.Fields = append(r.Fields, f)
		}
		return r
	}
	shape := &Shape{Repeated: repeated(pt.root, false)}
	named := 0
	for _, b := range pt.branches {
		if b.Kind == regexpplan.Capture {
			named++
		}
	}
	if named > 0 && named < len(pt.branches) {
		for _, b := range pt.branches {
			if b.Kind != regexpplan.Capture {
				return nil, &PatternError{b.Offset, "every branch of " + name + " is a named capture, or none is: name this branch, or group the alternation as ( … ) for a record"}
			}
		}
	}
	if named > 0 {
		shape.Union = true
		for _, b := range pt.branches {
			f := &Field{Name: b.Name, Tag: tags[b], Count: One}
			if len(scopeCaptures(b.Children[0], nil)) > 0 {
				f.Record = record(b.Name, b)
			}
			shape.Variants = append(shape.Variants, f)
		}
		return shape, nil
	}
	// The whole pattern is the outermost scope.
	shape.Record = record(name, &regexpplan.Node{Kind: regexpplan.Capture, Children: []*regexpplan.Node{pt.root}})
	return shape, nil
}

// scopeBody is the subtree of a scope: a capture's child.
func scopeBody(scope *regexpplan.Node) *regexpplan.Node { return scope.Children[0] }

func repeated(n *regexpplan.Node, under bool) bool {
	if n.Kind == regexpplan.Capture && under {
		return true
	}
	if n.Kind == regexpplan.Repeat && (n.Max < 0 || n.Max > 1) {
		under = true
	}
	for _, c := range n.Children {
		if repeated(c, under) {
			return true
		}
	}
	return false
}

// repeatedGroups refuses a repeat whose body holds two or more named
// captures of one scope: name the repeated group so each occurrence is a
// record (Capture shapes 4).
func repeatedGroups(n *regexpplan.Node) *PatternError {
	if n.Kind == regexpplan.Repeat && (n.Max < 0 || n.Max > 1) && len(scopeCaptures(n.Children[0], nil)) >= 2 {
		return &PatternError{n.Offset, "name the repeated group, (?<Entry>…)*, so each occurrence is one record"}
	}
	for _, c := range n.Children {
		if err := repeatedGroups(c); err != nil {
			return err
		}
	}
	return nil
}
