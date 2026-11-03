package bindc

import (
	"strconv"
	"strings"
)

// GoSig is the Go signature of a typed function.
type GoSig struct {
	Params  []GoParam
	Results []GoResult
	// Status is true when the C result is the status type. The last Go
	// result is always an error: a status other than success, a call
	// before Load, a destroyed handle or a slice too long for its length.
	Status bool
}

// GoParam is one parameter of a typed Go function.
type GoParam struct {
	Name string
	Type string
	// C is the C parameter it stands for.
	C int
	// Class is the class of a pointer position, or "" for a plain value.
	Class Class
}

// GoResult is one result of a typed Go function other than its error.
type GoResult struct {
	Type string
	// C is the C parameter it comes from (an out or an out_count), or -1
	// for the C result.
	C int
}

// goReserved are names a parameter of a generated function may not take:
// Go keywords, predeclared identifiers, the imported packages and the
// locals of a generated body.
var goReserved = map[string]bool{}

func init() {
	for k := range goKeywords {
		goReserved[k] = true
	}
	for _, n := range strings.Fields(`bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64
		rune string uint uint8 uint16 uint32 uint64 uintptr any comparable true false iota nil append cap clear close
		complex copy delete imag len make max min new panic print println real recover
		purego unsafe runtime errors fmt sync`) {
		goReserved[n] = true
	}
}

// GoType is the Go type of a C type of a value: a field, a by-value
// parameter or result, an element or the target of an out-parameter.
func (b *Binding) GoType(t Type) string {
	switch t.Kind {
	case TypeScalar:
		return goScalar[t.Scalar]
	case TypeNamed:
		return b.GoTypes[t.Name]
	case TypeArray:
		return "[" + strconv.FormatInt(t.Len, 10) + "]" + b.GoType(*t.Elem)
	case TypePointer:
		if t.Elem.Kind == TypeNamed && b.k.handles[t.Elem.Name] != nil {
			return "*" + b.GoTypes[t.Elem.Name]
		}
		return "uintptr"
	}
	return "?"
}

// goScalar maps a C scalar to its Go type.
var goScalar = map[Scalar]string{
	Int8: "int8", Int16: "int16", Int32: "int32", Int64: "int64",
	Uint8: "uint8", Uint16: "uint16", Uint32: "uint32", Uint64: "uint64",
	Bool: "bool", Float32: "float32", Float64: "float64",
}

// goSig builds the Go signature of a typed function: handles, values,
// slices and plain values are parameters in C order (lengths and
// capacities drop out); the C result, then out-parameters and output counts
// in C order, are results; a status result is the final error.
func (b *Binding) goSig(bf *BoundFunc) *GoSig {
	f := bf.C
	s := &GoSig{}
	class := map[int]Position{}
	consumed := map[int]bool{}
	for _, q := range bf.Positions {
		class[q.Param] = q
		if q.Class == ClassInBuffer || q.Class == ClassOutBuffer {
			consumed[q.Length] = true
		}
	}
	if f.Result.Kind != TypeVoid {
		if f.Result.Kind == TypeNamed && f.Result.Name == b.Contract.Status.Type {
			s.Status = true
		} else {
			s.Results = append(s.Results, GoResult{Type: b.GoType(f.Result), C: -1})
		}
	}
	used := map[string]bool{}
	for i, p := range f.Params {
		q, isPos := class[i]
		switch {
		case consumed[i]:
			continue
		case !isPos:
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used), Type: b.GoType(p.Type), C: i})
		case q.Class == ClassHandle:
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used), Type: "*" + b.GoTypes[p.Type.Elem.Name], C: i, Class: q.Class})
		case q.Class == ClassValue:
			t := b.GoType(*p.Type.Elem)
			if p.Type.LenExpr != "" {
				t = "[" + strconv.FormatInt(p.Type.Len, 10) + "]" + t
			}
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used), Type: t, C: i, Class: q.Class})
		case q.Class == ClassInBuffer || q.Class == ClassOutBuffer:
			et := b.GoType(*p.Type.Elem)
			if et == "uint8" {
				et = "byte" // the same type; Go spells byte slices so
			}
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used), Type: "[]" + et, C: i, Class: q.Class})
		case q.Class == ClassOut:
			s.Results = append(s.Results, GoResult{Type: b.GoType(*p.Type.Elem), C: i})
		case q.Class == ClassOutCount:
			s.Results = append(s.Results, GoResult{Type: b.GoType(*p.Type.Elem), C: i})
		}
	}
	return s
}

// paramName is the Go name of a parameter: its C name in lowerCamelCase,
// or for an unnamed one the name of its type, made unique in the function
// and kept off goReserved with a final "_".
func paramName(p Param, i int, b *Binding, used map[string]bool) string {
	n := lowerCamel(p.Name)
	if n == "" {
		t := p.Type
		for t.Kind == TypePointer {
			t = *t.Elem
		}
		if t.Kind == TypeNamed {
			n = lowerCamel(strings.TrimPrefix(t.Name, b.Contract.Prefixes.Type))
		}
		if n == "" {
			n = "arg" + strconv.Itoa(i)
		}
	}
	for goReserved[n] {
		n += "_"
	}
	base := n
	for k := 2; used[n]; k++ {
		n = base + strconv.Itoa(k)
	}
	used[n] = true
	return n
}

// String is the signature as Go source: "func Name(a T, b []U) (V, error)".
func (s *GoSig) String(name string) string {
	var b strings.Builder
	b.WriteString("func " + name + "(")
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name + " " + p.Type)
	}
	b.WriteString(")")
	res := s.ResultTypes()
	switch len(res) {
	case 0:
	case 1:
		b.WriteString(" " + res[0])
	default:
		b.WriteString(" (" + strings.Join(res, ", ") + ")")
	}
	return b.String()
}

// ResultTypes are the Go result types, the error last.
func (s *GoSig) ResultTypes() []string {
	var out []string
	for _, r := range s.Results {
		out = append(out, r.Type)
	}
	return append(out, "error")
}
