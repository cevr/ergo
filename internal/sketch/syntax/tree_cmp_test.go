package syntax

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// The seeded sources of the decoder property, and the tree comparison of
// the round trip (roundtrip_test.go).

// pickDecode is pickParse's source, with every line ended by a carriage
// return and a newline for one seed in three: the text of a comment and of
// a raw string drops them.
func pickDecode(seed int64, corpus []agreeInput) string {
	src := pickParse(seed, corpus)
	if seed%3 == 2 {
		src = strings.ReplaceAll(src, "\n", "\r\n")
	}
	return src
}

// sameTree is "" when want and got are equal, or the path of the first
// field that differs. A nil slice equals an empty one. Pointers pair one
// to one: a pointer of want met again must meet the pointer of got it met
// first, and no two pointers of want pair with one of got. Positions are
// left out: every Pos, and the File's source and line table.
func sameTree(want, got reflect.Value) string {
	c := &treeCmp{pairs: map[uintptr]uintptr{}, back: map[uintptr]uintptr{}}
	return c.cmp("File", want, got)
}

type treeCmp struct {
	pairs, back map[uintptr]uintptr
}

var fileType = reflect.TypeFor[File]()

func (c *treeCmp) cmp(path string, w, g reflect.Value) string {
	if w.Type() != g.Type() {
		return fmt.Sprintf("%s: type %s, got %s", path, w.Type(), g.Type())
	}
	if w.Type() == posType {
		return ""
	}
	switch w.Kind() {
	case reflect.Interface:
		if w.IsNil() || g.IsNil() {
			if w.IsNil() != g.IsNil() {
				return fmt.Sprintf("%s: nil %v, got nil %v", path, w.IsNil(), g.IsNil())
			}
			return ""
		}
		return c.cmp(path, w.Elem(), g.Elem())
	case reflect.Pointer:
		if w.IsNil() || g.IsNil() {
			if w.IsNil() != g.IsNil() {
				return fmt.Sprintf("%s: nil %v, got nil %v", path, w.IsNil(), g.IsNil())
			}
			return ""
		}
		wp, gp := w.Pointer(), g.Pointer()
		if p, ok := c.pairs[wp]; ok {
			if p != gp {
				return fmt.Sprintf("%s: a node met again pairs with another node of got", path)
			}
			return ""
		}
		if _, ok := c.back[gp]; ok {
			return fmt.Sprintf("%s: two nodes pair with one node of got", path)
		}
		c.pairs[wp], c.back[gp] = gp, wp
		return c.cmp(path, w.Elem(), g.Elem())
	case reflect.Struct:
		for i := 0; i < w.NumField(); i++ {
			if name := w.Type().Field(i).Name; w.Type() == fileType && (name == "Src" || name == "lines") {
				continue
			}
			if d := c.cmp(path+"."+w.Type().Field(i).Name, w.Field(i), g.Field(i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.Slice:
		if w.Len() != g.Len() {
			return fmt.Sprintf("%s: len %d, got %d", path, w.Len(), g.Len())
		}
		for i := 0; i < w.Len(); i++ {
			if d := c.cmp(path+"["+strconv.Itoa(i)+"]", w.Index(i), g.Index(i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.String:
		if w.String() != g.String() {
			return fmt.Sprintf("%s: %q, got %q", path, w.String(), g.String())
		}
	case reflect.Bool:
		if w.Bool() != g.Bool() {
			return fmt.Sprintf("%s: %v, got %v", path, w.Bool(), g.Bool())
		}
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int8, reflect.Int16:
		if w.Int() != g.Int() {
			return fmt.Sprintf("%s: %d, got %d", path, w.Int(), g.Int())
		}
	default:
		return fmt.Sprintf("%s: a %s the comparison does not read", path, w.Kind())
	}
	return ""
}
