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
	used := goFuncScope(b.pkg)
	for i, p := range f.Params {
		q, isPos := class[i]
		switch {
		case consumed[i]:
			continue
		case !isPos:
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used, f), Type: b.GoType(p.Type), C: i})
		case q.Class == ClassHandle:
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used, f), Type: "*" + b.GoTypes[p.Type.Elem.Name], C: i, Class: q.Class})
		case q.Class == ClassValue:
			t := b.GoType(*p.Type.Elem)
			if p.Type.LenExpr != "" {
				t = "[" + strconv.FormatInt(p.Type.Len, 10) + "]" + t
			}
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used, f), Type: t, C: i, Class: q.Class})
		case q.Class == ClassInBuffer || q.Class == ClassOutBuffer:
			et := b.GoType(*p.Type.Elem)
			if et == "uint8" {
				et = "byte" // the same type; Go spells byte slices so
			}
			s.Params = append(s.Params, GoParam{Name: paramName(p, i, b, used, f), Type: "[]" + et, C: i, Class: q.Class})
		case q.Class == ClassOut:
			s.Results = append(s.Results, GoResult{Type: b.GoType(*p.Type.Elem), C: i})
		case q.Class == ClassOutCount:
			s.Results = append(s.Results, GoResult{Type: b.GoType(*p.Type.Elem), C: i})
		}
	}
	return s
}

// paramName is the Go name of a parameter: its C name in lowerCamelCase,
// or for an unnamed one the name of its type, allocated in the function's
// scope (names.go): a reserved name gets a final "_", a taken one a number.
func paramName(p Param, i int, b *Binding, scope *nameScope, f *Function) string {
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
	return scope.alloc(n, "parameter "+positionName(f, i)+" of "+f.Name)
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
