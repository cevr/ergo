package lint

import (
	"fmt"
	"reflect"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// shapeOf is the meaning-level text of a syntax tree, for the rules that
// ask whether two pieces of code are the same (identical-arms,
// duplicate-test, duplicate-shape): the tree's node kinds and their
// operators, nested; a name as the object it resolves to, so two
// same-spelled names of different objects differ; a name the tree itself
// declares as the order of its declaration, so bodies that differ only in
// their locals' names are the same; a literal as its constant value. No
// position, comment or layout enters it, so a reformatted tree keeps its
// shape. Two trees with equal shapes do the same thing. Several nodes
// (a parameter list and a body, a list of statements) share one
// numbering of their declarations.
func shapeOf[N syntax.Node](info *check.Info, nodes ...N) string {
	s := shaper{info: info, locals: map[check.Object]int{}}
	for _, n := range nodes {
		s.node(n)
		s.b.WriteByte(';')
	}
	return s.b.String()
}

// shapeOfEdit is shapeOf of a piece of a body (an added hunk), whose
// variables may be declared outside it: a variable it uses but does not
// declare (a parameter, a local of the enclosing body) is numbered by its
// first use, as a declared one is, so one edit made in two bodies has one
// shape.
func shapeOfEdit(info *check.Info, nodes ...syntax.Node) string {
	s := shaper{info: info, locals: map[check.Object]int{}, free: true}
	for _, n := range nodes {
		s.node(n)
		s.b.WriteByte(';')
	}
	return s.b.String()
}

type shaper struct {
	info   *check.Info
	locals map[check.Object]int
	b      strings.Builder
	// free numbers the variables the tree uses and does not declare.
	free bool
}

var posType = reflect.TypeOf(syntax.Pos(0))

func (s *shaper) node(n syntax.Node) {
	if n == nil {
		return
	}
	if v := reflect.ValueOf(n); v.Kind() == reflect.Pointer && v.IsNil() {
		return
	}
	switch x := n.(type) {
	case *syntax.Ident:
		s.ident(x)
		return
	case *syntax.BasicLit:
		if v, ok := s.info.Values[x]; ok && v != nil {
			fmt.Fprintf(&s.b, "(lit %s)", v.ExactString())
		} else {
			fmt.Fprintf(&s.b, "(lit %d %q)", x.Kind, x.Value)
		}
		return
	}
	v := reflect.ValueOf(n)
	fmt.Fprintf(&s.b, "(%s", v.Type().Elem().Name())
	if e := v.Elem(); e.Kind() == reflect.Struct {
		for i := 0; i < e.NumField(); i++ {
			f := e.Field(i)
			if !e.Type().Field(i).IsExported() || f.Type() == posType {
				continue
			}
			switch f.Kind() {
			case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.String:
				fmt.Fprintf(&s.b, " %s=%v", e.Type().Field(i).Name, f.Interface())
			}
		}
	}
	syntax.Inspect(n, func(c syntax.Node) bool {
		if c == n {
			return true
		}
		s.b.WriteByte(' ')
		s.node(c)
		return false
	})
	s.b.WriteByte(')')
}

func (s *shaper) ident(x *syntax.Ident) {
	if o := s.info.Defs[x]; o != nil {
		if _, seen := s.locals[o]; !seen {
			s.locals[o] = len(s.locals)
		}
		fmt.Fprintf(&s.b, "(def %d)", s.locals[o])
		return
	}
	if o := s.info.Uses[x]; o != nil {
		if i, local := s.locals[o]; local {
			fmt.Fprintf(&s.b, "(local %d)", i)
		} else if _, isVar := o.(*check.Var); isVar && s.free {
			s.locals[o] = len(s.locals)
			fmt.Fprintf(&s.b, "(local %d)", s.locals[o])
		} else {
			fmt.Fprintf(&s.b, "(obj %p)", o)
		}
		return
	}
	// A name the checker resolves by its context, such as a field of a
	// selector or a composite literal's key: its spelling, read only
	// beside the resolved operand it belongs to.
	fmt.Fprintf(&s.b, "(name %q)", x.Name)
}
