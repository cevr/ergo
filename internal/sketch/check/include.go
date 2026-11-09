package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// include is one `..R` member of a record declaration (types.md, Structs
// 7): the fields of the record R take its place, in R's order, as the
// record's own fields, each keeping R's declaration (Field.Origin).
type include struct {
	member *syntax.Field
	at     int    // the index in the record's own fields where R's go
	target *Named // R, possibly an instance (..Page[T])
}

// includeTarget checks the type t of the member `..R`: R is a record
// declared in this package. It returns R, or nil after a diagnostic.
func (c *checker) includeTarget(member *syntax.Field, t Type) *Named {
	if isInvalid(t) {
		return nil
	}
	n, ok := t.(*Named)
	if !ok || n.origin().Obj.Decl == nil {
		c.errorf(member.Type, CodeType, "..%s includes the fields of a record; %s is not a record (types.md, Structs 7)", t, t)
		return nil
	}
	o := n.origin()
	if o.Obj.Pkg != c.pkg {
		c.errorf(member.Type, CodeType, "..%s includes a record of this package; %s is declared in package %s: write a field, %s %s (types.md, Structs 7)", t, t, o.Obj.Pkg.Name, o.Obj.Name, t)
		return nil
	}
	if _, isRecord := o.Obj.Decl.Spec.(*syntax.StructType); !isRecord {
		c.errorf(member.Type, CodeType, "..%s includes the fields of a record; %s is %s (types.md, Structs 7)", t, t, shapeNoun(o.Obj.Decl.Spec))
		return nil
	}
	return n
}

// expandIncludes splices the fields of every included record into each
// record of the package that includes it. It runs after every declaration
// of the package has its body, so an included record that holds the
// including one through a field (`type A { X []B }`, `type B { ..A }`)
// needs no order; a record resolved later (a local declaration) is
// expanded when it resolves (resolveNamed).
//
// A defined type took its base's body when it resolved (definedType): a
// defined type over an instance of a record with includes (`type M
// Listing[int]`) took a copy made before the splice, so every defined
// record type takes its base's body again, its base first.
func (c *checker) expandIncludes() {
	spliced := false
	for _, tn := range c.pkg.Types {
		if n, ok := tn.Type.(*Named); ok {
			spliced = c.expandInclude(n) || spliced
		}
	}
	c.includesExpanded = true
	if !spliced {
		return
	}
	again := map[*Named]bool{}
	var retake func(n *Named)
	retake = func(n *Named) {
		if again[n] {
			return
		}
		again[n] = true
		base, ok := n.def.(*Named)
		if !ok {
			return
		}
		retake(base.origin())
		if _, record := n.under.(*Struct); !record {
			return
		}
		if s, ok := under(base).(*Struct); ok && s != n.under {
			n.under = s
			c.resetInstances(n)
		}
	}
	for _, tn := range c.pkg.Types {
		if n, ok := tn.Type.(*Named); ok {
			retake(n)
		}
	}
}

// resetInstances clears the bodies that instances of n made from an old
// body of n; the next read makes them again.
func (c *checker) resetInstances(n *Named) {
	for _, cache := range []map[int32]*Named{c.instance, c.inferInstances} {
		for _, inst := range cache {
			if inst.Orig == n {
				inst.under = nil
			}
		}
	}
}

// expandInclude splices the included records' fields into n's record, the
// included records first. An inclusion cycle, a name given twice (also
// through two paths to one record) and an included record with no fields
// are S108 at the member. Instances of n made before the splice are reset,
// so their fields are made again from the whole record. It reports whether
// it spliced.
func (c *checker) expandInclude(n *Named) bool {
	s, ok := n.under.(*Struct)
	if !ok || len(s.includes) == 0 || s.expanding {
		return false
	}
	s.expanding = true
	saved := c.cur
	c.cur = n.Obj.file
	defer func() { c.cur = saved }()
	own := s.Fields
	from := map[string]string{} // a field name -> what gives it
	for _, f := range own {
		from[f.Name] = "an own field"
	}
	var fields []*Field
	next := 0
	for _, inc := range s.includes {
		fields = append(fields, own[next:inc.at]...)
		next = inc.at
		o := inc.target.origin()
		if os, isStruct := o.under.(*Struct); isStruct && os.expanding {
			c.errorf(inc.member, CodeType, "%s includes itself through ..%s: a record must not contain itself (types.md, Structs 4 and 7)", n.Obj.Name, inc.target)
			continue
		}
		c.expandInclude(o)
		ts, _ := under(inc.target).(*Struct)
		if ts == nil {
			continue // refused where it is declared
		}
		if len(ts.Fields) == 0 {
			c.errorf(inc.member, CodeType, "..%s includes no field: %s has none; drop the member (types.md, Structs 7)", inc.target, inc.target)
			continue
		}
		for _, f := range ts.Fields {
			if prev, dup := from[f.Name]; dup {
				c.errorf(inc.member, CodeType, "..%s gives the field %s, which %s already gives; a name has one declaration in a record (types.md, Structs 7)", inc.target, f.Name, prev)
				continue
			}
			from[f.Name] = fmt.Sprintf("..%s", inc.target)
			fields = append(fields, &Field{Name: f.Name, Type: f.Type, Pos: f.Pos, origin: f.Origin()})
		}
	}
	fields = append(fields, own[next:]...)
	s.Fields, s.index, s.includes, s.expanding = fields, nil, nil, false
	c.resetInstances(n)
	return true
}
