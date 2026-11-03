package bindc

import (
	"fmt"
	"sort"
	"strings"
)

// Code is the stable code of a diagnostic. Codes is the one table of codes.
type Code string

// The diagnostic codes. Each refused construct has one code; the parser
// reports one diagnostic per construct, at the token that starts it or
// that makes it fall outside the subset.
const (
	CodeSyntax          Code = "BC001"
	CodeUnknownType     Code = "BC002"
	CodeLexical         Code = "BC003"
	CodeImplicitWidth   Code = "BC010"
	CodeEnum            Code = "BC011"
	CodeUnion           Code = "BC012"
	CodeBitField        Code = "BC013"
	CodeVarargs         Code = "BC014"
	CodeFlexibleArray   Code = "BC015"
	CodeNestedStruct    Code = "BC016"
	CodeStructTag       Code = "BC017"
	CodeStorageClass    Code = "BC018"
	CodeAttribute       Code = "BC019"
	CodeConditional     Code = "BC020"
	CodeFunctionMacro   Code = "BC021"
	CodeKAndR           Code = "BC022"
	CodeDirective       Code = "BC023"
	CodeMacroBody       Code = "BC024"
	CodeConstantRange   Code = "BC025"
	CodeDuplicate       Code = "BC026"
	CodeQualifier       Code = "BC027"
	CodeDeclarator      Code = "BC028"
	CodeVariable        Code = "BC029"
	CodeStructure       Code = "BC030"
	CodeMacroIdentifier Code = "BC031"
	CodeMissingInclude  Code = "BC032"
)

// Codes describes every diagnostic code.
var Codes = map[Code]string{
	CodeSyntax:          "syntax error",
	CodeUnknownType:     "unknown type name",
	CodeLexical:         "character or token outside the subset (digraph, trigraph, line continuation, unterminated comment or literal, non-ASCII byte)",
	CodeImplicitWidth:   "integer type with an implementation-defined width (int, char, short, long, signed, unsigned); use a fixed-width type",
	CodeEnum:            "enum is outside the subset",
	CodeUnion:           "union is outside the subset",
	CodeBitField:        "bit field is outside the subset",
	CodeVarargs:         "variadic function is outside the subset",
	CodeFlexibleArray:   "flexible array member is outside the subset",
	CodeNestedStruct:    "struct definition inside a struct is outside the subset",
	CodeStructTag:       "struct named by its tag outside `typedef struct`; use the typedef name",
	CodeStorageClass:    "storage class, function specifier or function definition is outside the subset",
	CodeAttribute:       "attribute or compiler extension is outside the subset",
	CodeConditional:     "conditional directive other than the include guard and `#ifdef __cplusplus`",
	CodeFunctionMacro:   "function-like macro is outside the subset",
	CodeKAndR:           "K&R declaration; write a prototype (use `(void)` for no parameters)",
	CodeDirective:       "directive or include outside the subset",
	CodeMacroBody:       "macro body outside the integer constant subset",
	CodeConstantRange:   "constant out of range or with undefined behavior",
	CodeDuplicate:       "duplicate name",
	CodeQualifier:       "qualifier outside the subset (only `const T *`)",
	CodeDeclarator:      "type or declarator outside the subset",
	CodeVariable:        "variable declaration is outside the subset",
	CodeStructure:       "header structure outside the subset (include guard, extern \"C\" block)",
	CodeMacroIdentifier: "macro name used as an identifier",
	CodeMissingInclude:  "type or macro used before its standard header is included",
}

// Pos is a position in a header: 1-based line and byte column.
type Pos struct {
	File string
	Line int
	Col  int
}

func (p Pos) String() string { return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col) }

func (p Pos) before(q Pos) bool { return p.Line < q.Line || p.Line == q.Line && p.Col < q.Col }

// Diagnostic is one positioned error.
type Diagnostic struct {
	Pos  Pos
	Code Code
	Msg  string
}

func (d Diagnostic) String() string { return fmt.Sprintf("%s: %s %s", d.Pos, d.Code, d.Msg) }

// Diagnostics is the error a failed parse returns.
type Diagnostics []Diagnostic

func (ds Diagnostics) Error() string {
	lines := make([]string, len(ds))
	for i, d := range ds {
		lines[i] = d.String()
	}
	return strings.Join(lines, "\n")
}

type diags struct{ list Diagnostics }

func (d *diags) add(p Pos, c Code, format string, args ...any) {
	if _, ok := Codes[c]; !ok {
		panic("bindc: unknown diagnostic code " + string(c))
	}
	d.list = append(d.list, Diagnostic{Pos: p, Code: c, Msg: fmt.Sprintf(format, args...)})
}

func (d *diags) sorted() Diagnostics {
	sort.SliceStable(d.list, func(i, j int) bool { return d.list[i].Pos.before(d.list[j].Pos) })
	return d.list
}
