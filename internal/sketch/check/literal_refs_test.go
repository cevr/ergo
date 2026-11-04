package check_test

import (
	"fmt"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The literal-reference rule (fuzz-863c): every identifier inside a
// composite literal has one meaning, and typing (Info) and the reference
// walk agree on it, also inside a literal whose type typing refuses. The
// walk reads a literal's kind from its written type through the type
// declarations, and an elided literal's kind from the enclosing literal
// (rows.md, Inference 6). A record's value is a field, which never elides
// its type (types.md, Structs 3), so an elided literal there is refused
// untyped and the walk only visits it. Where typing refuses a literal's
// type, its values are orphans, typed with no expected type (call.go
// orphan), and an elided literal among them takes the kind the walk reads.
//
// TestLiteralRefsAgree crosses the literal's type, the element's shape and
// the class of the name inside, and checks what Info and the walk make of
// the name against an oracle written from those rules, never from the
// checker. The element positions are derived from the syntax AST as
// TestBinderNamesAgree derives the binder fields: the fields of
// CompositeLit and KeyValueExpr in lawScopeDomain.
//
// The domain's written types name package declarations. Each program also
// runs after a refused binder of the name its type is written with
// (`let Rec = v`: a refused type test, whose placeholder hides the type
// from a read only). A type position sees through a refused binder (rows.md
// Totality 6; fuzz-863c's `type y{}` and `let y = 1; y[0]{{t} }`), so the
// oracle is the same with it as without it.

// litWalk is a literal's kind as the walk reads it (reflits.go litKind).
type litWalk int

const (
	walkRecord      litWalk = iota // a record: keys are field names
	walkListRecord                 // a slice or map whose element type is a record
	walkListUnknown                // a slice whose element type is a type parameter
	walkUnknown                    // not decided: every element is walked as a value
	walkRefused                    // an elided literal where none may be elided
)

// elem is the kind of an elided literal that is an element of a literal of
// kind k.
func (k litWalk) elem() litWalk {
	switch k {
	case walkRecord, walkRefused:
		return walkRefused // a field never elides its type
	case walkListRecord:
		return walkRecord
	}
	return walkUnknown // a type parameter's kind is not decided
}

// litTyping is what typing makes of a literal.
type litTyping int

const (
	typedRecord        litTyping = iota // a record literal
	typedList                           // a slice of records
	typedOrphan                         // its type is refused: its values are orphans
	typedRefused                        // an elided literal refused untyped
	typedListOfRefused                  // a slice or map whose element type is refused
)

// elem is typing's verdict on an elided literal that is an element of a
// literal typing made t, which the walk reads as w.
func (t litTyping) elem(w litWalk) litTyping {
	switch t {
	case typedList:
		return typedRecord
	case typedListOfRefused:
		// The element's type is refused, so the elided literal's type is
		// refused: its values are orphans, of the kind the walk reads.
		return typedOrphan
	case typedOrphan:
		if w == walkRefused {
			return typedRefused // an orphan's elided literal follows the walk
		}
		return typedOrphan
	}
	return typedRefused // a record's field, or inside a refused literal
}

// litTypeForms are the literal types crossed: valid, and refused in each
// way a written type is refused, for a record and for a slice.
var litTypeForms = []struct {
	name   string
	src    string // the written type
	key    string // a key of a keyed element
	walk   litWalk
	typing litTyping
}{
	{"a valid record", "Rec", "V", walkRecord, typedRecord},
	{"a valid generic record instance", "Box[int]", "V", walkRecord, typedRecord},
	{"a record given type arguments", "Rec[int]", "V", walkRecord, typedOrphan},
	{"a generic record with the wrong arity", "Box[int, int]", "V", walkRecord, typedOrphan},
	{"an unknown name", "Missing", "V", walkUnknown, typedOrphan},
	{"a valid slice", "Recs", "0", walkListRecord, typedList},
	{"a valid generic slice instance", "Seq[Rec]", "0", walkListUnknown, typedList},
	{"a slice given type arguments", "Recs[int]", "0", walkListRecord, typedOrphan},
	{"a generic slice with the wrong arity", "Seq[Rec, Rec]", "0", walkListUnknown, typedOrphan},
	{"a slice of a record given type arguments", "[]Rec[int]", "0", walkListRecord, typedListOfRefused},
	{"a valid map", litMapType, "1", walkListRecord, typedList},
}

// litRefusedBinders are the lines a program starts with: none, and, when
// the literal's type is written with a declared type's name, a let of that
// name, which is a refused type test over the int v (a placeholder).
func litRefusedBinders(src string) []string {
	head, _, _ := strings.Cut(src, "[")
	if head == "" || head == "Missing" {
		return []string{""}
	}
	return []string{"", "let " + head + " = v\n\t"}
}

// litMapType is the map type of litTypeForms. A map literal's elements are
// key: value pairs; typing refuses a positional one, an orphan, of the
// kind the walk reads.
const litMapType = "Recm"

// litLeaf is what holds the name at the element.
type litLeaf int

const (
	leafName      litLeaf = iota // the name itself
	leafComposite                // a nested composite literal with a written type, Rec{$N}
	leafFuncLit                  // a function literal's body
)

var litLeaves = []struct {
	name string
	leaf litLeaf
	src  string
}{
	{"the name", leafName, "$N"},
	{"a nested composite literal", leafComposite, "Rec{$N}"},
	{"a function literal", leafFuncLit, "func() { let _ = $N }"},
}

// litShapes are the element positions, each a field of the syntax AST: a
// positional element (CompositeLit.Elts) or a keyed element's value
// (KeyValueExpr.Value), crossed with how many elided literals hold the leaf
// (none, an elided element, a nested elided element).
var litShapes = map[string]string{
	"CompositeLit.Elts":  "positional",
	"KeyValueExpr.Value": "keyed",
}

// litNoShape are the fields of the domain that hold no element value, each
// with its reason.
var litNoShape = map[string]string{
	"CompositeLit.Type": "the literal's type: the crossed type axis (litTypeForms)",
	"KeyValueExpr.Key":  "a record literal's key is a field name, no reference (rows.md, Inference 6); a slice's index is a value as its element is",
}

// litRefDecls are the declarations every program shares, and the names
// of each class.
const litRefDecls = `type Rec { V int }
type Box[T] { V T }
type Recs []Rec
type Seq[T] []T
type Recm Map[int, Rec]
type Line { N int }
func fx() {}
let pv = 5

`

// litNameClass is a class of name inside the literal.
type litNameClass int

const (
	nameFunc    litNameClass = iota // a package function: a reference edge
	nameValue                       // a package value: a reference edge
	nameType                        // a package type: no edge
	nameLocal                       // a local: no edge
	namePrelude                     // a prelude constant: no edge
)

var litNames = []struct {
	name  string
	class litNameClass
}{
	{"fx", nameFunc}, {"pv", nameValue}, {"Line", nameType}, {"v", nameLocal}, {"true", namePrelude},
}

// litRule is the oracle's verdict, in RefEdge's words, for a name of class
// n in a leaf whose enclosing literal typing makes t and the walk reads as
// w.
func litRule(t litTyping, w litWalk, leaf litLeaf, n litNameClass) (info, walk string) {
	typed := t != typedRefused
	if leaf == leafFuncLit && t == typedOrphan {
		// An orphan function literal needs the missing type: typing records
		// its frame skipped and types nothing in it (call.go orphan); the
		// walk keeps its uses apart (refs.go guardedUse) for class G.
		typed = false
	}
	info, walk = "none", "visited"
	if typed {
		switch n {
		case nameFunc, nameValue:
			info = "edge"
		case nameLocal:
			info = "local"
		default:
			info = "names"
		}
	}
	if w != walkRefused {
		walk = "value"
		if n == nameFunc || n == nameValue {
			walk = "edge"
		}
	}
	return info, walk
}

func TestLiteralRefsAgree(t *testing.T) {
	var fields []string
	for _, f := range lawScopeDomain(t) {
		if strings.HasPrefix(f, "CompositeLit.") || strings.HasPrefix(f, "KeyValueExpr.") {
			fields = append(fields, f)
		}
	}
	if len(fields) < 3 {
		t.Fatalf("found the literal fields %v; the guard reads the syntax package", fields)
	}
	inDomain := map[string]bool{}
	for _, f := range fields {
		inDomain[f] = true
		_, shaped := litShapes[f]
		_, reason := litNoShape[f]
		if shaped == reason {
			t.Errorf("the literal field %s needs exactly one of an element shape and a reason", f)
		}
	}
	for f := range litShapes {
		if !inDomain[f] {
			t.Errorf("litShapes names %s, no literal field of the domain", f)
		}
	}
	for f := range litNoShape {
		if !inDomain[f] {
			t.Errorf("litNoShape names %s, no literal field of the domain", f)
		}
	}

	programs, edges := 0, 0
	for _, ty := range litTypeForms {
		for _, field := range fields {
			shape, ok := litShapes[field]
			if !ok {
				continue
			}
			for depth := 0; depth <= 2; depth++ {
				for _, lf := range litLeaves {
					// The element: depth elided literals around the leaf.
					inner := lf.src
					for range depth {
						inner = "{" + inner + "}"
					}
					elem := inner
					if shape == "keyed" {
						elem = ty.key + ": " + inner
					}
					w, tp := ty.walk, ty.typing
					if shape == "positional" && ty.src == litMapType {
						tp = typedOrphan
					}
					for range depth {
						w = w.elem()
						tp = tp.elem(w)
					}
					for _, nm := range litNames {
						for _, refused := range litRefusedBinders(ty.src) {
							programs++
							body := "func host(v int) {\n\t" + refused + "let _ = " + ty.src + "{" + elem + "}\n}\n"
							at := len(litRefDecls) + strings.Index(body, "$N")
							src := litRefDecls + strings.Replace(body, "$N", nm.name, 1)
							label := fmt.Sprintf("%s, %s, depth %d, %s, %s", ty.name, shape, depth, lf.name, nm.name)
							if refused != "" {
								label += ", after " + strings.TrimSpace(refused)
							}

							wantInfo, wantWalk := litRule(tp, w, lf.leaf, nm.class)
							if wantInfo == "edge" {
								edges++
							}
							info, walk, found := check.RefEdge(src, at)
							if !found {
								t.Errorf("%s: no identifier at the name\n%s", label, src)
								continue
							}
							if info != wantInfo || walk != wantWalk {
								t.Errorf("%s: want Info %s and the walk %s, got Info %s and the walk %s\n%s", label, wantInfo, wantWalk, info, walk, src)
							}
							if p := check.RefWalkProblems(src); len(p) > 0 {
								t.Errorf("%s: reference walk problems %v\n%s", label, p, src)
							}
						}
					}
				}
			}
		}
	}
	if edges == 0 {
		t.Fatal("no program has a reference edge")
	}
	t.Logf("%d literal fields, %d programs, %d with an Info edge", len(fields), programs, edges)
}
