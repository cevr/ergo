package bindc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The oracle: OpenTUI's native-abi.generated.ts, which its build writes
// from Zig's translate-c of opentui.h. This file reads it with its own
// small reader and maps model types to its kind names with its own table;
// it shares no code with the parser or the layout.

type tsSymbol struct {
	args    []string
	returns string
}

type tsField struct {
	name                string
	offset, size, align int64
	typ                 string
}

type tsLayout struct {
	size, align int64
	fields      []tsField
}

type translateC struct {
	symbols   map[string]tsSymbol
	callbacks map[string]tsSymbol
	layouts   map[string]tsLayout
	constants map[string]string
}

// tsSection returns the body of `export const <name> = {` ... `} as const`.
func tsSection(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "export const "+name+" = {\n")
	if start < 0 {
		t.Fatalf("native-abi.generated.ts: no section %s", name)
	}
	body := src[start+len("export const "+name+" = {\n"):]
	end := strings.Index(body, "\n} as const")
	if end < 0 {
		t.Fatalf("native-abi.generated.ts: section %s does not end", name)
	}
	return body[:end+1]
}

var (
	tsSymbolRe = regexp.MustCompile(`(?s)^\s*(\w+): \{\s*args: \[([^\]]*)\],\s*returns: "(\w+)",?\s*\},\n`)
	tsLayoutRe = regexp.MustCompile(`(?s)^\s*(\w+): \{\s*size: (\d+),\s*alignment: (\d+),\s*fields: \{\n(.*?)\n\s*\},\n\s*\},\n`)
	tsFieldRe  = regexp.MustCompile(`^\s*(\w+): \{ offset: (\d+), size: (\d+), alignment: (\d+), type: "([^"]+)" \},$`)
	tsConstRe  = regexp.MustCompile(`^\s*(\w+): (-?\d+)n?,$`)
)

// readSymbols reads every entry of a symbol section; any text that is not
// an entry fails the test, so no entry is skipped silently.
func readSymbols(t *testing.T, body, section string) map[string]tsSymbol {
	t.Helper()
	out := map[string]tsSymbol{}
	for body != "" {
		m := tsSymbolRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s: cannot read %.80q", section, body)
		}
		body = body[len(m[0]):]
		var args []string
		for _, a := range strings.Split(m[2], ",") {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, strings.Trim(a, `"`))
			}
		}
		if _, dup := out[m[1]]; dup {
			t.Fatalf("%s: %s twice", section, m[1])
		}
		out[m[1]] = tsSymbol{args: args, returns: m[3]}
	}
	return out
}

func atoi(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func readTranslateC(t *testing.T) *translateC {
	t.Helper()
	src := strings.ReplaceAll(string(readInput(t, "testdata/opentui/native-abi.generated.ts")), "\r\n", "\n")
	tc := &translateC{
		symbols:   readSymbols(t, tsSection(t, src, "nativeSymbols"), "nativeSymbols"),
		callbacks: readSymbols(t, tsSection(t, src, "nativeCallbacks"), "nativeCallbacks"),
		layouts:   map[string]tsLayout{},
		constants: map[string]string{},
	}
	body := tsSection(t, src, "nativeLayouts")
	for body != "" {
		m := tsLayoutRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("nativeLayouts: cannot read %.80q", body)
		}
		body = body[len(m[0]):]
		l := tsLayout{size: atoi(t, m[2]), align: atoi(t, m[3])}
		for _, line := range strings.Split(m[4], "\n") {
			f := tsFieldRe.FindStringSubmatch(line)
			if f == nil {
				t.Fatalf("nativeLayouts %s: cannot read field %q", m[1], line)
			}
			l.fields = append(l.fields, tsField{f[1], atoi(t, f[2]), atoi(t, f[3]), atoi(t, f[4]), f[5]})
		}
		tc.layouts[m[1]] = l
	}
	for _, line := range strings.Split(strings.TrimSuffix(tsSection(t, src, "nativeConstants"), "\n"), "\n") {
		m := tsConstRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("nativeConstants: cannot read %q", line)
		}
		tc.constants[m[1]] = m[2]
	}
	return tc
}

// tsScalar is Bun FFI's kind name of each scalar.
var tsScalar = map[Scalar]string{
	Int8: "i8", Int16: "i16", Int32: "i32", Int64: "i64",
	Uint8: "u8", Uint16: "u16", Uint32: "u32", Uint64: "u64",
	Bool: "bool", Float32: "f32", Float64: "f64",
}

// tsKinds are the kinds translate-c may give a position of type t in a
// symbol: a scalar its own kind, a callback "ptr", and a data pointer
// "ptr" or "buffer". The choice between the two is OpenTUI's pointer policy
// (packages/core/scripts/native-abi-pointers.ts), not a C fact.
func tsKinds(m *Model, t Type) []string {
	switch t.Kind {
	case TypeVoid:
		return []string{"void"}
	case TypeScalar:
		return []string{tsScalar[t.Scalar]}
	case TypePointer:
		return []string{"ptr", "buffer"}
	case TypeNamed:
		d := findType(m, t.Name)
		switch d.Kind {
		case DeclScalar:
			return []string{tsScalar[d.Scalar]}
		case DeclCallback:
			return []string{"ptr"}
		}
	}
	return []string{"(no kind for " + t.Spell() + ")"}
}

// tsFieldType is translate-c's type name of a struct field: "u32",
// "[4]u16", "ot_handle".
func tsFieldType(m *Model, t Type) string {
	switch t.Kind {
	case TypeScalar:
		return tsScalar[t.Scalar]
	case TypeArray:
		return "[" + strconv.FormatInt(t.Len, 10) + "]" + tsFieldType(m, *t.Elem)
	case TypeNamed:
		if d := findType(m, t.Name); d.Kind == DeclScalar {
			return tsScalar[d.Scalar]
		}
		return t.Name
	case TypePointer:
		return "ptr"
	}
	return "?"
}

func findType(m *Model, name string) TypeDecl {
	for _, d := range m.Types {
		if d.Name == name {
			return d
		}
	}
	return TypeDecl{}
}

func oneOf(s string, set []string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

func checkSymbol(t *testing.T, m *Model, what, name string, result Type, params []Param, want tsSymbol) {
	t.Helper()
	if len(params) != len(want.args) {
		t.Errorf("%s %s: %d parameters, translate-c has %d", what, name, len(params), len(want.args))
		return
	}
	for i, p := range params {
		if k := tsKinds(m, p.Type); !oneOf(want.args[i], k) {
			t.Errorf("%s %s parameter %d (%s): kind %q, translate-c has %q", what, name, i, p.Type.Spell(), k, want.args[i])
		}
	}
	if k := tsKinds(m, result); !oneOf(want.returns, k) {
		t.Errorf("%s %s result (%s): kind %q, translate-c has %q", what, name, result.Spell(), k, want.returns)
	}
}

func TestOpenTUIMatchesTranslateC(t *testing.T) {
	tc := readTranslateC(t)
	m := parseHeader(t, "testdata/opentui", "opentui.h")

	// The counts of the census (state.md of the plan): the oracle itself
	// must not shrink unnoticed.
	counts := []struct {
		what      string
		got, want int
	}{
		{"translate-c symbols", len(tc.symbols), 259},
		{"translate-c callbacks", len(tc.callbacks), 3},
		{"translate-c layouts", len(tc.layouts), 93},
		{"translate-c constants", len(tc.constants), 701},
	}
	for _, c := range counts {
		if c.got != c.want {
			t.Errorf("%s: %d, want %d", c.what, c.got, c.want)
		}
	}

	// Symbols: the same names, and per position the same kind.
	seen := map[string]bool{}
	for _, f := range m.Functions {
		seen[f.Name] = true
		want, ok := tc.symbols[f.Name]
		if !ok {
			t.Errorf("function %s: not in translate-c", f.Name)
			continue
		}
		checkSymbol(t, m, "function", f.Name, f.Result, f.Params, want)
	}
	for name := range tc.symbols {
		if !seen[name] {
			t.Errorf("translate-c symbol %s: not in the model", name)
		}
	}

	// Callbacks and layouts.
	ncb, nlayout := 0, 0
	for _, d := range m.Types {
		switch d.Kind {
		case DeclCallback:
			ncb++
			want, ok := tc.callbacks[d.Name]
			if !ok {
				t.Errorf("callback %s: not in translate-c", d.Name)
				continue
			}
			checkSymbol(t, m, "callback", d.Name, *d.Result, d.Params, want)
		case DeclStruct:
			nlayout++
			want, ok := tc.layouts[d.Name]
			if !ok {
				t.Errorf("struct %s: not in translate-c", d.Name)
				continue
			}
			if d.Size != want.size || d.Align != want.align {
				t.Errorf("struct %s: size %d align %d, translate-c has size %d align %d", d.Name, d.Size, d.Align, want.size, want.align)
			}
			if len(d.Fields) != len(want.fields) {
				t.Errorf("struct %s: %d fields, translate-c has %d", d.Name, len(d.Fields), len(want.fields))
				continue
			}
			for i, f := range d.Fields {
				w := want.fields[i]
				got := tsField{f.Name, f.Offset, f.Size, f.Align, tsFieldType(m, f.Type)}
				if got != w {
					t.Errorf("struct %s field %d: %+v, translate-c has %+v", d.Name, i, got, w)
				}
			}
		}
	}
	if ncb != len(tc.callbacks) || nlayout != len(tc.layouts) {
		t.Errorf("model has %d callbacks and %d struct layouts, translate-c has %d and %d", ncb, nlayout, len(tc.callbacks), len(tc.layouts))
	}

	// Constants: the same names and values.
	if len(m.Constants) != len(tc.constants) {
		t.Errorf("model has %d constants, translate-c has %d", len(m.Constants), len(tc.constants))
	}
	for _, c := range m.Constants {
		want, ok := tc.constants[c.Name]
		if !ok {
			t.Errorf("constant %s: not in translate-c", c.Name)
			continue
		}
		if c.Value() != want {
			t.Errorf("constant %s = %s, translate-c has %s", c.Name, c.Value(), want)
		}
	}
	if t.Failed() {
		return
	}
	t.Log(fmt.Sprintf("matched %d symbols, %d callbacks, %d layouts, %d constants", len(m.Functions), ncb, nlayout, len(m.Constants)))
}
