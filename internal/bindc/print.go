package bindc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Print writes m as a header of the subset in canonical form: the include
// guard, the includes, the extern "C" block, then every constant, every
// type and every function in model order. Parse(m.Header, Print(m)) equals
// m for every model Parse returns, and Print is a fixed point of
// Parse after Print (TestRoundTripProperty).
func Print(m *Model) []byte {
	var b strings.Builder
	if m.Guard != "" {
		fmt.Fprintf(&b, "#ifndef %s\n#define %s\n\n", m.Guard, m.Guard)
	}
	for _, inc := range m.Includes {
		fmt.Fprintf(&b, "#include <%s>\n", inc)
	}
	if len(m.Includes) > 0 {
		b.WriteString("\n")
	}
	if m.CPlusPlus {
		b.WriteString("#ifdef __cplusplus\nextern \"C\" {\n#endif\n\n")
	}
	for _, c := range m.Constants {
		fmt.Fprintf(&b, "#define %s %s\n", c.Name, c.Expr)
	}
	if len(m.Constants) > 0 {
		b.WriteString("\n")
	}
	for _, d := range m.Types {
		switch d.Kind {
		case DeclScalar:
			fmt.Fprintf(&b, "typedef %s %s;\n", d.Scalar, d.Name)
		case DeclOpaque:
			fmt.Fprintf(&b, "typedef struct %s %s;\n", d.Tag, d.Name)
		case DeclStruct:
			b.WriteString("typedef struct ")
			if d.Tag != "" {
				b.WriteString(d.Tag + " ")
			}
			b.WriteString("{\n")
			for _, f := range d.Fields {
				fmt.Fprintf(&b, "    %s;\n", f.Type.Declare(f.Name))
			}
			fmt.Fprintf(&b, "} %s;\n", d.Name)
		case DeclCallback:
			fmt.Fprintf(&b, "typedef %s (*%s)(%s);\n", d.Result.Spell(), d.Name, printParams(d.Params))
		}
	}
	if len(m.Types) > 0 {
		b.WriteString("\n")
	}
	for _, f := range m.Functions {
		fmt.Fprintf(&b, "%s(%s);\n", f.Result.Declare(f.Name), printParams(f.Params))
	}
	if len(m.Functions) > 0 {
		b.WriteString("\n")
	}
	if m.CPlusPlus {
		b.WriteString("#ifdef __cplusplus\n}\n#endif\n")
	}
	if m.Guard != "" {
		b.WriteString("\n#endif\n")
	}
	return []byte(b.String())
}

func printParams(ps []Param) string {
	if len(ps) == 0 {
		return "void"
	}
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.Type.Declare(p.Name)
	}
	return strings.Join(s, ", ")
}

// JSON is the model as indented JSON (the model.json golden), with a final
// newline. Constant values are decimal strings, so a reader without 64-bit
// integers keeps them exact.
func JSON(m *Model) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		panic("bindc: " + err.Error())
	}
	return buf.Bytes()
}

// MarshalJSON writes a type as its C spelling (Spell), which names every
// fact of the type.
func (t Type) MarshalJSON() ([]byte, error) { return json.Marshal(t.Spell()) }

// MarshalJSON writes a constant with its value as a decimal string.
func (c Constant) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Name  string `json:"name"`
		Type  Scalar `json:"type"`
		Value string `json:"value"`
		Expr  string `json:"expr"`
	}{c.Name, c.Type, c.Value(), c.Expr})
}
