package bindc

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

var (
	propCases = flag.Int("bindc.cases", 1000, "cases per property")
	propSeed  = flag.Uint64("bindc.seed", 20261103, "seed of the properties")
)

// TestRoundTripProperty: for a generated model m, Parse(Print(m)) equals m,
// and Print is a fixed point of Parse after Print. The model comes from a
// generated header with random spacing, comments and declaration order.
func TestRoundTripProperty(t *testing.T) {
	var constants, structs, fields, arrays, callbacks, funcs, params, unnamed, arrayParams int
	defer func() {
		t.Logf("%d cases: %d constants, %d structs with %d fields (%d arrays), %d callbacks, %d functions with %d parameters (%d unnamed, %d arrays)",
			*propCases, constants, structs, fields, arrays, callbacks, funcs, params, unnamed, arrayParams)
	}()
	count := func(ps []Param) {
		for _, p := range ps {
			params++
			if p.Name == "" {
				unnamed++
			}
			if p.Type.LenExpr != "" {
				arrayParams++
			}
		}
	}
	for i := 0; i < *propCases; i++ {
		seed := *propSeed + uint64(i)
		h := newGen(seed).header(4 + i%40)
		m, err := Parse("gen.h", []byte(h.src))
		if err != nil {
			t.Fatalf("seed %d: the generated header does not parse:\n%v\n%s", seed, err, h.src)
		}
		constants += len(m.Constants)
		for _, d := range m.Types {
			switch d.Kind {
			case DeclStruct:
				structs++
				for _, f := range d.Fields {
					fields++
					if f.Type.Kind == TypeArray {
						arrays++
					}
				}
			case DeclCallback:
				callbacks++
				count(d.Params)
			}
		}
		for _, f := range m.Functions {
			funcs++
			count(f.Params)
		}
		printed := Print(m)
		m2, err := Parse("gen.h", printed)
		if err != nil {
			t.Fatalf("seed %d: the printed model does not parse:\n%v\n%s", seed, err, printed)
		}
		if !reflect.DeepEqual(m, m2) {
			t.Fatalf("seed %d: Parse(Print(m)) != m\nm:  %s\nm2: %s\nprinted:\n%s", seed, JSON(m), JSON(m2), printed)
		}
		if again := Print(m2); !bytes.Equal(again, printed) {
			t.Fatalf("seed %d: Print(Parse(Print(m))) != Print(m):\n%s", seed, firstDiff(printed, again))
		}
	}
}

// refused is one construct outside the subset. text is inserted at a
// place of kind where; `@` marks where the one diagnostic must point, and
// each `$` becomes a fresh name.
type refused struct {
	where string // "top", "field" or "param"
	code  Code
	text  string
}

// refusedConstructs covers every construct the spec refuses (int, char,
// long, short, unsigned alone; enum; union; bit fields; varargs; flexible
// array members; nested structs; static and inline definitions;
// attributes; conditionals; function-like macros; K&R declarations), and
// the other edges of the subset.
var refusedConstructs = []refused{
	{"top", CodeImplicitWidth, "typedef @unsigned $;"},
	{"top", CodeImplicitWidth, "typedef @unsigned int $;"},
	{"top", CodeImplicitWidth, "@int $(void);"},
	{"top", CodeImplicitWidth, "@long long $(uint32_t a);"},
	{"top", CodeImplicitWidth, "typedef @char $;"},
	{"top", CodeImplicitWidth, "@short $(void);"},
	{"top", CodeImplicitWidth, "@signed $(void);"},
	{"top", CodeImplicitWidth, "uint32_t $(@unsigned a, uint32_t b);"},
	{"field", CodeImplicitWidth, "    @unsigned $;"},
	{"field", CodeImplicitWidth, "    @int $;"},
	{"field", CodeImplicitWidth, "    @char $[4];"},
	{"field", CodeImplicitWidth, "    @long $, $;"},
	{"param", CodeImplicitWidth, "@unsigned $"},
	{"param", CodeImplicitWidth, "const @char *$"},
	{"param", CodeImplicitWidth, "@short"},

	{"top", CodeEnum, "@enum $ { $, $ };"},
	{"top", CodeEnum, "typedef @enum { $ } $;"},
	{"top", CodeEnum, "typedef @enum $ $;"},
	{"field", CodeEnum, "    @enum { $ } $;"},
	{"param", CodeEnum, "@enum $ $"},

	{"top", CodeUnion, "typedef @union { uint32_t a; float b; } $;"},
	{"top", CodeUnion, "@union $ { uint32_t a; };"},
	{"field", CodeUnion, "    @union { uint32_t a; float b; } $;"},
	{"param", CodeUnion, "@union $ *$"},

	{"field", CodeBitField, "    uint32_t $ @: 3;"},
	{"field", CodeBitField, "    uint8_t $ @:1, $ : 7;"},

	{"top", CodeVarargs, "uint32_t $(uint32_t a, @...);"},
	{"param", CodeVarargs, "@..."},

	{"field", CodeFlexibleArray, "    uint8_t $@[];"},

	{"field", CodeNestedStruct, "    @struct { uint32_t a; } $;"},
	{"field", CodeNestedStruct, "    @struct $ { uint32_t a; } $;"},
	{"field", CodeNestedStruct, "    @struct { uint32_t a; uint32_t b; };"},
	{"top", CodeNestedStruct, "typedef struct $ { uint32_t a; @struct { uint32_t b; } c; } $;"},

	{"top", CodeStructTag, "@struct $ { uint32_t a; };"},
	{"top", CodeStructTag, "@struct $;"},
	{"field", CodeStructTag, "    @struct $ *$;"},
	{"param", CodeStructTag, "const @struct $ *$"},

	{"top", CodeStorageClass, "@static inline uint32_t $(void) { return 0; }"},
	{"top", CodeStorageClass, "@static uint32_t $(uint32_t x) { if (x) { return 1; } return x; }"},
	{"top", CodeStorageClass, "@inline uint32_t $(void);"},
	{"top", CodeStorageClass, "@extern uint32_t $(void);"},
	{"top", CodeStorageClass, "@static uint32_t $;"},
	{"top", CodeStorageClass, "uint32_t $(void) @{ return 0; }"},
	{"param", CodeStorageClass, "@register uint32_t $"},

	{"top", CodeAttribute, "@__attribute__((visibility(\"default\"))) uint32_t $(void);"},
	{"top", CodeAttribute, "uint32_t $(void) @__attribute__((pure));"},
	{"top", CodeAttribute, "@[[nodiscard]] uint32_t $(void);"},
	{"top", CodeAttribute, "@__declspec(dllexport) uint32_t $(void);"},
	{"top", CodeAttribute, "typedef struct $ { uint32_t a; } @__attribute__((packed)) $;"},
	{"field", CodeAttribute, "    uint32_t $ @__attribute__((aligned(8)));"},
	{"field", CodeAttribute, "    @_Alignas(8) uint32_t $;"},
	{"param", CodeAttribute, "@__attribute__((unused)) uint32_t $"},

	{"top", CodeConditional, "@#if 1\nuint32_t $(void);\n#endif"},
	{"top", CodeConditional, "@#ifdef $\nuint32_t $(void);\n#else\nuint64_t $(void);\n#endif"},
	{"top", CodeConditional, "@#ifndef $\n#define $ 1\n#endif"},
	{"top", CodeConditional, "@#if defined($) && $ > 1\n#if 2\n#endif\n#endif"},
	{"top", CodeConditional, "@#ifdef __cplusplus\nuint32_t $(void);\n#endif"},
	{"top", CodeConditional, "@#else"},

	{"top", CodeFunctionMacro, "#define @$(x) ((x) + 1)"},
	{"top", CodeFunctionMacro, "#define @$() 1"},

	{"top", CodeKAndR, "uint32_t @$();"},
	{"top", CodeKAndR, "uint32_t @$(a, b);"},
	{"top", CodeKAndR, "typedef void (*@$)();"},

	{"top", CodeDirective, "@#include <stdio.h>"},
	{"top", CodeDirective, "@#include \"other.h\""},
	{"top", CodeDirective, "@#pragma once"},
	{"top", CodeDirective, "@#undef $"},
	{"top", CodeDirective, "@#error no"},
	{"top", CodeDirective, "@#line 10"},

	{"top", CodeMacroBody, "#define $ @1u"},
	{"top", CodeMacroBody, "#define $ @1.5"},
	{"top", CodeMacroBody, "#define $ @\"s\""},
	{"top", CodeMacroBody, "#define $ @'a'"},
	{"top", CodeMacroBody, "#define $ @sizeof(uint32_t)"},
	{"top", CodeMacroBody, "#define $ (1 @/ 2)"},
	{"top", CodeMacroBody, "#define $ (1 @>> 2)"},
	{"top", CodeMacroBody, "#define $ (1 @& 2)"},
	{"top", CodeMacroBody, "#define $ (1 @? 2 : 3)"},
	{"top", CodeMacroBody, "#define $ @010"},
	{"top", CodeMacroBody, "#define $ @~1"},
	{"top", CodeMacroBody, "#define $ @+1"},
	{"top", CodeMacroBody, "#define $ @$"},
	{"top", CodeMacroBody, "#define $ UINT32_C(@-1)"},
	{"top", CodeMacroBody, "#define $ (1 + 2@"},
	{"top", CodeMacroBody, "#define $ @uint32_t"},

	{"top", CodeConstantRange, "#define $ (INT32_C(2147483647) @+ 1)"},
	{"top", CodeConstantRange, "#define $ (1 @<< 40)"},
	{"top", CodeConstantRange, "#define $ (-1 @<< 2)"},
	{"top", CodeConstantRange, "#define $ @99999999999999999999"},
	{"top", CodeConstantRange, "#define $ UINT8_C(@256)"},
	{"top", CodeConstantRange, "#define $ (INT64_C(9223372036854775807) @* 2)"},
	{"field", CodeConstantRange, "    uint8_t $[@0];"},

	{"top", CodeQualifier, "@volatile uint32_t $(void);"},
	{"top", CodeQualifier, "uint32_t * @const $(void);"},
	{"top", CodeQualifier, "@const uint32_t $(void);"},
	{"top", CodeQualifier, "uint32_t $(@const uint32_t a);"},
	{"field", CodeQualifier, "    @const uint32_t $;"},
	{"field", CodeQualifier, "    uint8_t * @restrict $;"},
	{"field", CodeQualifier, "    uint32_t @volatile $;"},
	{"param", CodeQualifier, "uint8_t * @restrict $"},
	{"param", CodeQualifier, "@_Atomic uint32_t $"},

	{"top", CodeDeclarator, "uint32_t @(*$)(void);"},
	{"top", CodeDeclarator, "typedef uint32_t $@[4];"},
	{"top", CodeDeclarator, "typedef uint32_t *@$;"},
	{"top", CodeDeclarator, "typedef void @$;"},
	{"top", CodeDeclarator, "typedef @_Bool $;"},
	{"top", CodeDeclarator, "typedef uint32_t $@, $;"},
	{"top", CodeDeclarator, "typedef uint32_t (@$)(void);"},
	{"field", CodeDeclarator, "    uint32_t @(*$)(void);"},
	{"field", CodeDeclarator, "    void @$;"},
	{"field", CodeDeclarator, "    uint32_t $[2]@[2];"},
	{"param", CodeDeclarator, "uint32_t $@[]"},
	{"param", CodeDeclarator, "uint32_t @(*$)(void)"},
	{"param", CodeDeclarator, "void @$"},

	{"top", CodeVariable, "uint32_t @$;"},
	{"top", CodeVariable, "uint32_t @$ = 1;"},

	{"top", CodeUnknownType, "@size_t $(void);"},
	{"param", CodeUnknownType, "@FILE *$"},

	{"top", CodeDuplicate, "uint32_t $(void);\nuint32_t @$1(void);"},
	{"top", CodeDuplicate, "typedef uint32_t $;\ntypedef struct $1 @$1;"},
	{"top", CodeDuplicate, "#define $ 1\n#define @$1 2"},
	{"top", CodeDuplicate, "typedef struct $ $;\ntypedef struct @$1 { uint32_t a; } $;"},
	{"field", CodeDuplicate, "    uint32_t $;\n    uint32_t @$1;"},
	{"param", CodeDuplicate, "uint32_t $, uint32_t @$1"},
	{"top", CodeDuplicate, "@#include <stdint.h>"},
	{"top", CodeMacroIdentifier, "#define $ 1\nuint32_t $(uint32_t @$1);"},
	{"top", CodeMacroIdentifier, "#define $ 1\nuint32_t @$1(void);"},
	{"top", CodeMacroIdentifier, "#define $ 1\ntypedef struct @$1 $;"},
	{"top", CodeMacroIdentifier, "uint32_t @UINT32_MAX(void);"},
	{"top", CodeMacroIdentifier, "uint32_t @INT32_C(uint32_t x);"},
	{"top", CodeMacroIdentifier, "uint32_t @linux(void);"},
	{"top", CodeMacroIdentifier, "typedef struct @_Tag $;"},
	{"top", CodeMacroIdentifier, "#define @UINT32_MAX 5"},
	{"top", CodeMacroIdentifier, "#define @__GNUC__ 5"},
	{"top", CodeMacroIdentifier, "#define @float 5"},
	{"field", CodeMacroIdentifier, "    uint32_t @__x;"},
	{"param", CodeMacroIdentifier, "uint32_t @NULL"},

	{"top", CodeLexical, "uint32_t $(void); @`"},
	{"top", CodeLexical, "/* ok */ uint32_t $(void @??) ;"},
	{"top", CodeLexical, "#define $ 1 @\\\n"},
}

// expand replaces each `$` with a fresh name and each `$1` with the name
// the first `$` got, and removes the `@` marker. It returns the text and
// the byte offset of the marker.
func expand(text string, fresh func() string) (string, int) {
	var b strings.Builder
	at, first := -1, ""
	for i := 0; i < len(text); i++ {
		switch {
		case text[i] == '@':
			at = b.Len()
		case text[i] == '$' && i+1 < len(text) && text[i+1] == '1':
			b.WriteString(first)
			i++
		case text[i] == '$':
			name := fresh()
			if first == "" {
				first = name
			}
			b.WriteString(name)
		default:
			b.WriteByte(text[i])
		}
	}
	return b.String(), at
}

func lineCol(src string, off int) (int, int) {
	line := 1 + strings.Count(src[:off], "\n")
	return line, off - strings.LastIndexByte(src[:off], '\n')
}

// TestUnsupportedProperty: each refused construct, put into a generated
// valid header at a generated place, gives exactly one diagnostic, with
// its code and at its position, and no model.
func TestUnsupportedProperty(t *testing.T) {
	cases := *propCases
	if cases < 8*len(refusedConstructs) {
		cases = 8 * len(refusedConstructs)
	}
	for i := 0; i < cases; i++ {
		rc := refusedConstructs[i%len(refusedConstructs)]
		seed := *propSeed + uint64(i)
		g := newGen(seed)
		h := g.header(2 + i%20)
		if _, err := Parse("gen.h", []byte(h.src)); err != nil {
			t.Fatalf("seed %d: the generated header does not parse:\n%v\n%s", seed, err, h.src)
		}
		var places []int
		var sep string
		switch rc.where {
		case "top":
			places, sep = h.top, "\n"
		case "field":
			places, sep = h.field, "\n"
		case "param":
			places, sep = h.param, ", "
		}
		place := places[g.r.IntN(len(places))]
		n := 0
		text, at := expand(rc.text, func() string { n++; return fmt.Sprintf("zz_refused%d", n) })
		if at < 0 {
			t.Fatalf("construct %q has no @ marker", rc.text)
		}
		src := h.src[:place] + text + sep + h.src[place:]
		line, col := lineCol(src, place+at)
		m, err := Parse("gen.h", []byte(src))
		var ds Diagnostics
		errors.As(err, &ds)
		want := fmt.Sprintf("gen.h:%d:%d: %s", line, col, rc.code)
		if m != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].String(), want+" ") {
			t.Fatalf("seed %d, construct %q at %s: want one diagnostic %s and no model; got model %v and:\n%v\n--- source:\n%s",
				seed, rc.text, rc.where, want, m != nil, err, numbered(src))
		}
	}
}

func numbered(src string) string {
	lines := strings.Split(src, "\n")
	for i := range lines {
		lines[i] = fmt.Sprintf("%3d| %s", i+1, lines[i])
	}
	return strings.Join(lines, "\n")
}

// wholeHeaders are refusals that depend on the whole header, so they have
// no random place: the structure of the guard and the extern "C" block,
// and a standard type or macro used before its include. `@` marks the
// position of the one diagnostic.
var wholeHeaders = []struct {
	code Code
	src  string
}{
	{CodeStructure, "@#ifndef G\n#define G\n#include <stdint.h>\nuint32_t f(void);\n"},
	{CodeStructure, "#include <stdint.h>\n@#ifdef __cplusplus\nextern \"C\" {\n#endif\nuint32_t f(void);\n"},
	{CodeStructure, "#include <stdint.h>\nuint32_t f(void);\n@#ifdef __cplusplus\nextern \"C\" {\n#endif\n"},
	{CodeStructure, "#ifndef G\n#define G\n#include <stdint.h>\n#endif\n@uint32_t f(void);\n"},
	{CodeStructure, "#ifndef G\n#define G\n#include <stdint.h>\n#endif\n@#define X 1\n"},
	{CodeStructure, "#include <stdint.h>\n#ifdef __cplusplus\nextern \"C\" {\n#endif\nuint32_t f(void);\n#ifdef __cplusplus\n}\n#endif\n@uint32_t g(void);\n"},
	{CodeMissingInclude, "@uint32_t f(void);\n#include <stdint.h>\n"},
	{CodeMissingInclude, "#include <stdint.h>\ntypedef @bool flag;\n"},
	{CodeMissingInclude, "#define X @UINT32_C(1)\n"},
	{CodeSyntax, "#include <stdint.h>\nuint32_t f(void)@"},
	{CodeKAndR, "#include <stdint.h>\nuint32_t @f(void)\nuint32_t g(void);\n"},
	{CodeSyntax, "#include <stdint.h>\n@;\n"},
	{CodeSyntax, "#include <stdint.h>\ntypedef struct s { uint32_t a; } s;\nuint32_t f(s @if);\n"},
}

func TestWholeHeaderDiagnostics(t *testing.T) {
	for _, c := range wholeHeaders {
		at := strings.Index(c.src, "@")
		src := c.src[:at] + c.src[at+1:]
		line, col := lineCol(src, at)
		m, err := Parse("h.h", []byte(src))
		var ds Diagnostics
		errors.As(err, &ds)
		want := fmt.Sprintf("h.h:%d:%d: %s", line, col, c.code)
		if m != nil || len(ds) != 1 || !strings.HasPrefix(ds[0].String(), want+" ") {
			t.Errorf("want one diagnostic %s and no model; got model %v and:\n%v\n--- source:\n%s", want, m != nil, err, numbered(src))
		}
	}
}

// TestEveryCodeIsTested: every diagnostic code has a refusal case, so no
// code is dead or untested.
func TestEveryCodeIsTested(t *testing.T) {
	seen := map[Code]bool{}
	for _, rc := range refusedConstructs {
		seen[rc.code] = true
	}
	for _, c := range wholeHeaders {
		seen[c.code] = true
	}
	for c := range Codes {
		if !seen[c] {
			t.Errorf("code %s (%s) has no refusal case", c, Codes[c])
		}
	}
}
