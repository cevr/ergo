package hostsyntax

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// families are the node types the encoding carries: every struct of the
// syntax tree. TestFamiliesCoverSyntax holds the list to the syntax
// package, so a new node type cannot cross unnoticed; a type missing here
// is a U document, never a skipped node.
var families = map[reflect.Type]bool{}

func init() {
	for _, n := range []any{
		syntax.Comment{}, syntax.CommentGroup{}, syntax.File{}, syntax.ImportDecl{}, syntax.ImportSpec{},
		syntax.VarDecl{}, syntax.VarSpec{}, syntax.TypeDecl{}, syntax.FuncDecl{}, syntax.ContextDecl{},
		syntax.ContextBody{}, syntax.ContextEntry{}, syntax.Field{}, syntax.FieldList{}, syntax.NeedsClause{},
		syntax.TakesClause{}, syntax.ProvidesClause{}, syntax.Ident{}, syntax.BasicLit{}, syntax.CompositeLit{},
		syntax.KeyValueExpr{}, syntax.FuncLit{}, syntax.ParenExpr{}, syntax.SelectorExpr{}, syntax.IndexExpr{},
		syntax.SliceExpr{}, syntax.TypeGuard{}, syntax.CallExpr{}, syntax.StarExpr{}, syntax.UnaryExpr{},
		syntax.BinaryExpr{}, syntax.TryExpr{}, syntax.CatchExpr{}, syntax.ContextLit{}, syntax.ArrayType{},
		syntax.MapType{}, syntax.FuncType{}, syntax.StructType{}, syntax.InterfaceType{}, syntax.ErrorRow{},
		syntax.MachineType{}, syntax.TupleExpr{}, syntax.MachineEntry{}, syntax.CatchArm{}, syntax.DeclStmt{},
		syntax.ExprStmt{}, syntax.AssignStmt{}, syntax.ReturnStmt{}, syntax.BranchStmt{}, syntax.BlockStmt{},
		syntax.IfStmt{}, syntax.SwitchStmt{}, syntax.TypeSwitchStmt{}, syntax.ForStmt{}, syntax.RangeStmt{},
		syntax.CaseClause{}, syntax.ExternSpec{}, syntax.TestDecl{}, syntax.LawDecl{}, syntax.LawReach{},
		syntax.AssertStmt{}, syntax.VariantSpec{}, syntax.AsPattern{}, syntax.InterpolatedString{},
		// Not a Node, but a node's field points to it (LawDecl.Rewrite).
		syntax.LawRewrite{},
	} {
		families[reflect.TypeOf(n)] = true
	}
}

var posType = reflect.TypeFor[syntax.Pos]()

// unknownFamily stops an encoding at a node type the encoding does not carry.
type unknownFamily struct{ name string }

// encoder writes one document's nodes.
type encoder struct {
	ids   map[uintptr]int
	nodes []string
}

func encode(f *syntax.File) (encoding string) {
	e := &encoder{ids: map[uintptr]int{}}
	defer func() {
		if r := recover(); r != nil {
			u, ok := r.(unknownFamily)
			if !ok {
				panic(r)
			}
			encoding = Header + "U" + str(f.Src) + str(u.name)
		}
	}()
	root := e.node(reflect.ValueOf(f))
	var b strings.Builder
	b.WriteString(Header)
	b.WriteString("D")
	starts := lineStarts(f)
	b.WriteString("l" + strconv.Itoa(len(starts)) + ":")
	for _, s := range starts {
		b.WriteString("i" + strconv.Itoa(s) + ";")
	}
	b.WriteString("l" + strconv.Itoa(len(e.nodes)) + ":")
	for _, n := range e.nodes {
		b.WriteString(n)
	}
	b.WriteString(root)
	return b.String()
}

// lineStarts are the offsets where the parser's lines start: the offsets
// whose column File.Position reports as 1.
func lineStarts(f *syntax.File) []int {
	starts := []int{0}
	for off := 1; off <= len(f.Src); off++ {
		if _, col := f.Position(syntax.Pos(off)); col == 1 {
			starts = append(starts, off)
		}
	}
	return starts
}

func failure(src string, e *syntax.Error) string {
	return Header + "X" + str(src) + str(e.Code) + str(e.Message) +
		integer(e.Offset) + integer(e.Length) + integer(e.Line) + integer(e.Column)
}

func str(s string) string  { return "s" + strconv.Itoa(len(s)) + ":" + s }
func integer(n int) string { return "i" + strconv.Itoa(n) + ";" }

// node is the reference item of the node v points to, encoding it first
// when it was not reached before.
func (e *encoder) node(v reflect.Value) string {
	if id, ok := e.ids[v.Pointer()]; ok {
		return "c" + strconv.Itoa(id) + ";"
	}
	t := v.Elem().Type()
	if !families[t] {
		panic(unknownFamily{t.Name()})
	}
	id := len(e.nodes)
	e.ids[v.Pointer()] = id
	e.nodes = append(e.nodes, "")
	var b strings.Builder
	n := 0
	for i := range t.NumField() {
		if t.Field(i).IsExported() {
			n++
		}
	}
	b.WriteString("n" + str(t.Name()) + strconv.Itoa(n) + ":")
	for i := range t.NumField() {
		if !t.Field(i).IsExported() {
			continue
		}
		b.WriteString(str(t.Field(i).Name))
		b.WriteString(e.item(v.Elem().Field(i)))
	}
	e.nodes[id] = b.String()
	return "c" + strconv.Itoa(id) + ";"
}

func (e *encoder) item(v reflect.Value) string {
	if v.Type() == posType {
		if syntax.Pos(v.Int()) == syntax.NoPos {
			return "q"
		}
		return "p" + strconv.FormatInt(v.Int(), 10) + ";"
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return "t"
		}
		return "f"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "i" + strconv.FormatInt(v.Int(), 10) + ";"
	case reflect.String:
		return str(v.String())
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return "-"
		}
		if v.Kind() == reflect.Interface {
			return e.item(v.Elem())
		}
		return e.node(v)
	case reflect.Slice:
		var b strings.Builder
		b.WriteString("l" + strconv.Itoa(v.Len()) + ":")
		for i := range v.Len() {
			b.WriteString(e.item(v.Index(i)))
		}
		return b.String()
	}
	panic(fmt.Sprintf("hostsyntax: no encoding form for %s", v.Type()))
}
