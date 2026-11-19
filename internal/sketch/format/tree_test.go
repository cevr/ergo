package format

import (
	"fmt"
	"reflect"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

var (
	posType     = reflect.TypeOf(syntax.Pos(0))
	commentType = reflect.TypeOf((*syntax.CommentGroup)(nil))
)

// treeShape renders a syntax tree without positions and comments: node
// types, names, literals, operators and the order and nesting of children.
// Formatting must not change it, so two sources with one shape mean the
// same program (go/printer's tests compare go/ast trees the same way).
func treeShape(f *syntax.File) string {
	var b strings.Builder
	shape(&b, reflect.ValueOf(f), 0)
	return b.String()
}

func shape(b *strings.Builder, v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		if v.Kind() == reflect.Pointer && v.Type() == commentType {
			return
		}
		shape(b, v.Elem(), depth)
	case reflect.Struct:
		t := v.Type()
		b.WriteString(t.Name())
		b.WriteString("{")
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// A []Pos is positions too (OrPattern.Bars).
			if !f.IsExported() || f.Type == posType || f.Type == reflect.SliceOf(posType) || f.Type == commentType {
				continue
			}
			if t == reflect.TypeOf(syntax.File{}) && (f.Name == "Src" || f.Name == "Comments") {
				continue
			}
			fmt.Fprintf(b, "%s:", f.Name)
			shape(b, v.Field(i), depth+1)
			b.WriteString(" ")
		}
		b.WriteString("}")
	case reflect.Slice:
		b.WriteString("[")
		for i := 0; i < v.Len(); i++ {
			shape(b, v.Index(i), depth+1)
			b.WriteString(",")
		}
		b.WriteString("]")
	default:
		fmt.Fprintf(b, "%v", v.Interface())
	}
}
