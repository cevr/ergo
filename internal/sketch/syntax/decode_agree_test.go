package syntax

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestDecodeAgreesWithGo (capstone S2d): on every input of
// TestParseAgreesWithGo, ParseRows (compiler/syntax's parse, decoded)
// gives what Parse gives: the same *Error, or a tree equal field by field,
// unexported fields included, where each node of one tree pairs with one
// node of the other (sameTree), so a node two fields share in one tree is
// shared in the other, and a fresh copy stays a copy. A failing seed
// prints the source, the first field that differs and the replay command.
// It kills the decode rows of testdata/mutants.json.
func TestDecodeAgreesWithGo(t *testing.T) {
	corpus := corpusInputs(t)
	inputs := agreeSeeds(t, func(s int64) string { return pickDecode(s, corpus) })
	if os.Getenv("ERGO_AGREE_SEED") == "" {
		inputs = append(corpus, inputs...)
	}
	failures := 0
	for _, in := range inputs {
		want, werr := Parse(in.src)
		got, gerr := ParseRows(in.src)
		var diff string
		switch {
		case werr != nil || gerr != nil:
			if !reflect.DeepEqual(werr, gerr) {
				diff = fmt.Sprintf("errors: go %v, decoded %v", werr, gerr)
			}
		default:
			diff = sameTree(reflect.ValueOf(want), reflect.ValueOf(got))
		}
		if diff == "" {
			continue
		}
		failures++
		if failures <= 3 {
			src := in.src
			if len(src) > 600 && in.seed < 0 {
				src = src[:600] + "…"
			}
			t.Errorf("%s: the decoded tree differs\nsource %q\n%s%s", in.name, src, diff, replay(in, "TestDecodeAgreesWithGo"))
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d inputs differ", failures, len(inputs))
	}
}

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
// first, and no two pointers of want pair with one of got.
func sameTree(want, got reflect.Value) string {
	c := &treeCmp{pairs: map[uintptr]uintptr{}, back: map[uintptr]uintptr{}}
	return c.cmp("File", want, got)
}

type treeCmp struct {
	pairs, back map[uintptr]uintptr
}

func (c *treeCmp) cmp(path string, w, g reflect.Value) string {
	if w.Type() != g.Type() {
		return fmt.Sprintf("%s: type %s, decoded %s", path, w.Type(), g.Type())
	}
	switch w.Kind() {
	case reflect.Interface:
		if w.IsNil() || g.IsNil() {
			if w.IsNil() != g.IsNil() {
				return fmt.Sprintf("%s: nil %v, decoded nil %v", path, w.IsNil(), g.IsNil())
			}
			return ""
		}
		return c.cmp(path, w.Elem(), g.Elem())
	case reflect.Pointer:
		if w.IsNil() || g.IsNil() {
			if w.IsNil() != g.IsNil() {
				return fmt.Sprintf("%s: nil %v, decoded nil %v", path, w.IsNil(), g.IsNil())
			}
			return ""
		}
		wp, gp := w.Pointer(), g.Pointer()
		if p, ok := c.pairs[wp]; ok {
			if p != gp {
				return fmt.Sprintf("%s: a node met again pairs with another decoded node", path)
			}
			return ""
		}
		if _, ok := c.back[gp]; ok {
			return fmt.Sprintf("%s: two nodes pair with one decoded node", path)
		}
		c.pairs[wp], c.back[gp] = gp, wp
		return c.cmp(path, w.Elem(), g.Elem())
	case reflect.Struct:
		for i := 0; i < w.NumField(); i++ {
			if d := c.cmp(path+"."+w.Type().Field(i).Name, w.Field(i), g.Field(i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.Slice:
		if w.Len() != g.Len() {
			return fmt.Sprintf("%s: len %d, decoded %d", path, w.Len(), g.Len())
		}
		for i := 0; i < w.Len(); i++ {
			if d := c.cmp(path+"["+strconv.Itoa(i)+"]", w.Index(i), g.Index(i)); d != "" {
				return d
			}
		}
		return ""
	case reflect.String:
		if w.String() != g.String() {
			return fmt.Sprintf("%s: %q, decoded %q", path, w.String(), g.String())
		}
	case reflect.Bool:
		if w.Bool() != g.Bool() {
			return fmt.Sprintf("%s: %v, decoded %v", path, w.Bool(), g.Bool())
		}
	case reflect.Int, reflect.Int64, reflect.Int32, reflect.Int8, reflect.Int16:
		if w.Int() != g.Int() {
			return fmt.Sprintf("%s: %d, decoded %d", path, w.Int(), g.Int())
		}
	default:
		return fmt.Sprintf("%s: a %s the comparison does not read", path, w.Kind())
	}
	return ""
}
