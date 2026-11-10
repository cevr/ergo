package rt

import (
	"runtime"
	"strconv"
	"strings"
	"unsafe"
)

// Value texts without fmt (binsize BS2). fmt and reflect were 70 KB of
// every program because the defect report and the cause text wrote values
// with %v. Text writes what fmt's %v writes for the values an Ergo
// program can raise or fail with: a text, an error, a Go runtime error, a
// Stringer, a boolean and a number. A value of any other Go type comes
// only through import go; a program that imports go links the gofmt leaf
// of runtime/v1, which sets the host text to fmt's, and Text then writes
// every value as fmt does.

// hostText is fmt's text of v (%v, or with typed, the Go type's name and
// %+v); nil until a program links the gofmt leaf.
var hostText func(v any, typed bool) string

// UseHostText makes f the text of values Text does not know; the gofmt
// leaf calls it from its init. It returns the host text it replaced, so a
// test can take the core's own text and restore it.
func UseHostText(f func(v any, typed bool) string) (prev func(v any, typed bool) string) {
	prev, hostText = hostText, f
	return prev
}

// unknownText is the text of a value Text does not know when no host text
// is linked.
const unknownText = "(a Go value; its text needs fmt, which only a program with import go links)"

// Text is v's text as fmt's %v writes it.
func Text(v any) string {
	if hostText != nil {
		return hostText(v, false)
	}
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.FormatInt(int64(x), 10)
	case int8:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case uint8:
		return strconv.FormatUint(uint64(x), 10)
	case uint16:
		return strconv.FormatUint(uint64(x), 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case uintptr:
		return strconv.FormatUint(uint64(x), 10)
	case float32:
		return floatText(float64(x), 32)
	case float64:
		return floatText(x, 64)
	case error:
		return methodText(v, "Error", x.Error)
	case interface{ String() string }:
		return methodText(v, "String", x.String)
	}
	return unknownText
}

// TypedText is v's Go type name and its text as fmt's %+v writes it
// (Idle{n:1}), for a machine state in a defect. Only fmt and reflect can
// write a struct, so without the gofmt leaf it is unknownText.
func TypedText(v any) string {
	if hostText != nil {
		return hostText(v, true)
	}
	return unknownText
}

// floatText is fmt's %v of a float of the given bit size: the shortest
// 'g' form (+Inf, -Inf and NaN as strconv writes them).
func floatText(f float64, size int) string { return strconv.FormatFloat(f, 'g', -1, size) }

// methodText is the result of v's Error or String method, call, as fmt
// writes it: a panic in the method of a nil pointer is "<nil>", any other
// panic "%!v(PANIC=Error method: VALUE)".
func methodText(v any, method string, call func() string) (s string) {
	defer func() {
		if p := recover(); p != nil {
			if nilPointer(v) {
				s = "<nil>"
				return
			}
			s = "%!v(PANIC=" + method + " method: " + Text(p) + ")"
		}
	}()
	return call()
}

// typeProbe is a type no value outside rt has: TypeString asserts a value
// to it to read the value's type from the Go runtime's assertion error.
type typeProbe struct{}

// TypeString is the Go type of v as reflect's Type.String writes it
// (main.Failed, *v1.Panic, []int), without reflect (binsize BS2): the Go
// runtime writes it in the error of a failed type assertion. It is "nil"
// for a nil v. The trace sink names failures and machine states with it.
func TypeString(v any) string {
	if v == nil {
		return "nil"
	}
	return assertedType(v)
}

// assertedType is v's type string, read from the runtime's error of the
// assertion of v to typeProbe: "interface conversion: interface {} is
// TYPE, not rt.typeProbe".
func assertedType(v any) (s string) {
	if _, ok := v.(typeProbe); ok {
		return "rt.typeProbe"
	}
	defer func() {
		e, _ := recover().(*runtime.TypeAssertionError)
		msg := e.Error()
		const head, tail = "interface conversion: interface {} is ", ", not rt.typeProbe"
		if !strings.HasPrefix(msg, head) || !strings.HasSuffix(msg, tail) {
			panic("rt: the Go runtime's type assertion error changed its form: " + msg)
		}
		s = msg[len(head) : len(msg)-len(tail)]
	}()
	_ = v.(typeProbe)
	return ""
}

// TypeName is the name of the type whose string is s, as reflect's
// Type.Name gives it: the named type's name without its package (Failed,
// Box[int]), the name of a predeclared type (int), and "" for a type
// literal ([]int, *main.T, map[string]int, func(), struct {}).
func TypeName(s string) string {
	for _, lit := range []string{"*", "[", "map[", "chan ", "<-chan ", "func(", "struct {", "interface {"} {
		if strings.HasPrefix(s, lit) {
			return ""
		}
	}
	i, brackets := len(s)-1, 0
	for i >= 0 && (s[i] != '.' || brackets != 0) {
		switch s[i] {
		case ']':
			brackets++
		case '[':
			brackets--
		}
		i--
	}
	return s[i+1:]
}

// nilPointer reports whether v holds a nil pointer: an interface's data
// word is nil only for a nil pointer-shaped value (a pointer, map, channel
// or function), since any other value is stored behind a pointer.
func nilPointer(v any) bool {
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&v))[1] == nil
}
