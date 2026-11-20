package eval

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A module's own Go bodies in the licence run (LAWFFI LF2m; docs/spec/
// interop.md, Trusted Go 3). The evaluator has no native of a module's
// Go, and a law that reaches one would be skipped and license nothing;
// so ergo test compiles the effect-free bodies the program binds
// (check.ModuleBodySource) into a driver, a Go program that reads one
// call per line and writes its result, and runs two of it, one per
// ambient state of the licence run (licence.go). The verdict over a Go
// body is then the body's own run, as the built program runs it.
//
// The line protocol, which DriverSource writes the Go side of:
//
//	request: <target> <arg>...      reply: ok <result> | panic <hex text>
//
// An integer is its decimal, a float the hex of its IEEE bits, a bool 0
// or 1, a string "s" and its bytes' hex, and a slice its length and then
// its elements.

// GoBodies runs one request line on the driver of ambient state s (0 or
// 1) and gives its reply line; an error is the driver's failure, not the
// body's, and makes the case skipped.
type GoBodies interface {
	Call(s int, line string) (string, error)
}

// goBasics are the Go spellings of the basic types a driver reads and
// writes, by their token kind.
var goBasics = sync.OnceValue(func() map[string]string {
	return map[string]string{
		"int": "i", "int8": "i", "int16": "i", "int32": "i", "int64": "i", "rune": "i",
		"uint": "u", "uint8": "u", "uint16": "u", "uint32": "u", "uint64": "u", "uintptr": "u", "byte": "u",
		"float32": "f32", "float64": "f64", "bool": "b", "string": "s",
	}
})

// DriverSource is the Go text of the driver's main package over bodies,
// whose package i is imported from "<module>/b<i>"; ok is false when a
// body takes or returns a type outside goBasics and slices of them, or
// returns no one result.
func DriverSource(module string, bodies []check.GoBodySource) (string, bool) {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"bufio\"\n\t\"os\"\n\n\tdrv \"" + module + "/drv\"\n")
	for i := range bodies {
		fmt.Fprintf(&b, "\tb%d \"%s/b%d\"\n", i, module, i)
	}
	b.WriteString(")\n\nfunc main() {\n\tin := bufio.NewScanner(os.Stdin)\n\tin.Buffer(nil, 1<<30)\n\tout := bufio.NewWriter(os.Stdout)\n\tfor in.Scan() {\n\t\tr := drv.NewReader(in.Text())\n\t\tswitch r.Next() {\n")
	for i, s := range bodies {
		if len(s.Body.Results) != 1 || goBasics()[s.Body.Results[0]] == "" {
			return "", false
		}
		fmt.Fprintf(&b, "\t\tcase %q:\n", s.Target)
		var args []string
		for j, p := range s.Body.Params {
			read, ok := driverRead(p.Type)
			if !ok {
				return "", false
			}
			fmt.Fprintf(&b, "\t\t\ta%d := %s\n", j, read)
			args = append(args, fmt.Sprintf("a%d", j))
		}
		fmt.Fprintf(&b, "\t\t\tdrv.Reply(out, func() string { return drv.W%s(b%d.%s(%s)) })\n", goBasics()[s.Body.Results[0]], i, s.Name, strings.Join(args, ", "))
	}
	b.WriteString("\t\tdefault:\n\t\t\tdrv.Reply(out, func() string { panic(\"no such body\") })\n\t\t}\n\t\tout.Flush()\n\t}\n}\n")
	return b.String(), true
}

// driverRead is the Go expression that reads a value of the Go type t
// from the reader r.
func driverRead(t string) (string, bool) {
	if elem, ok := strings.CutPrefix(t, "[]"); ok {
		read, ok := driverRead(elem)
		if !ok || strings.HasPrefix(elem, "[]") {
			return "", false
		}
		return "drv.Slice(r, func(r *drv.Reader) " + elem + " { return " + read + " })", true
	}
	switch goBasics()[t] {
	case "i":
		return t + "(r.Int())", true
	case "u":
		return t + "(r.Uint())", true
	case "f32":
		return "r.F32()", true
	case "f64":
		return "r.F64()", true
	case "b":
		return "r.Bool()", true
	case "s":
		return "r.String()", true
	}
	return "", false
}

// DriverSupport is the Go text of the driver's package drv: the reader
// and writer of the line protocol.
const DriverSupport = `package drv

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Reader struct{ f []string }

func NewReader(line string) *Reader { return &Reader{f: strings.Fields(line)} }

func (r *Reader) Next() string {
	if len(r.f) == 0 {
		panic("short request")
	}
	s := r.f[0]
	r.f = r.f[1:]
	return s
}

func (r *Reader) Int() int64 { v, err := strconv.ParseInt(r.Next(), 10, 64); must(err); return v }
func (r *Reader) Uint() uint64 { v, err := strconv.ParseUint(r.Next(), 10, 64); must(err); return v }
func (r *Reader) F64() float64 { return math.Float64frombits(r.Uint()) }
func (r *Reader) F32() float32 { return math.Float32frombits(uint32(r.Uint())) }
func (r *Reader) Bool() bool { return r.Next() == "1" }
func (r *Reader) String() string {
	b, err := hex.DecodeString(strings.TrimPrefix(r.Next(), "s"))
	must(err)
	return string(b)
}

func Slice[T any](r *Reader, el func(*Reader) T) []T {
	n := int(r.Int())
	out := make([]T, n)
	for i := range out {
		out[i] = el(r)
	}
	return out
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func Wi[T ~int | ~int8 | ~int16 | ~int32 | ~int64](v T) string { return strconv.FormatInt(int64(v), 10) }
func Wu[T ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr](v T) string { return strconv.FormatUint(uint64(v), 10) }
func Wf64(v float64) string { return strconv.FormatUint(math.Float64bits(v), 10) }
func Wf32(v float32) string { return strconv.FormatUint(uint64(math.Float32bits(v)), 10) }
func Wb(v bool) string {
	if v {
		return "1"
	}
	return "0"
}
func Ws(v string) string { return "s" + hex.EncodeToString([]byte(v)) }

func Reply(out *bufio.Writer, call func() string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(out, "panic %s\n", hex.EncodeToString([]byte(fmt.Sprint(r))))
		}
	}()
	fmt.Fprintf(out, "ok %s\n", call())
}
`

// goBody reports a module body fn this run's driver serves.
func (in *interp) goBody(fn *check.Func) bool {
	_, ok := check.ModuleBodySource(fn)
	return ok && in.goBodies != nil
}

// goBodyNative is the native of a module body f in a licence run: one
// request to the driver of the current ambient state.
func (in *interp) goBodyNative(target string) native {
	return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		var line strings.Builder
		line.WriteString(target)
		for _, a := range args {
			if !encodeToken(&line, a) {
				return nil, fr.unsupported(at, "the Go body %s on a %T argument", target, a)
			}
		}
		reply, err := in.goBodies.Call(in.ambient.index, line.String())
		if err != nil {
			return nil, fr.unsupported(at, "the Go body %s: its driver failed: %v", target, err)
		}
		if text, ok := strings.CutPrefix(reply, "panic "); ok {
			why, _ := hex.DecodeString(text)
			return nil, fr.defect(at, "%s", why)
		}
		tok, ok := strings.CutPrefix(reply, "ok ")
		var v Value
		if ok && len(fn.Sig.Results) == 1 {
			v, ok = decodeToken(fn.Sig.Results[0], tok)
		}
		if !ok {
			return nil, fr.unsupported(at, "the Go body %s: its driver replied %q", target, reply)
		}
		return v, nil
	}
}

// encodeToken writes the tokens of v, a basic value or a slice of them.
func encodeToken(b *strings.Builder, v Value) bool {
	b.WriteByte(' ')
	switch x := v.(type) {
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case int8, int16, int32:
		fmt.Fprintf(b, "%d", x)
	case uint8, uint16, uint32, uint64:
		fmt.Fprintf(b, "%d", x)
	case float64:
		b.WriteString(strconv.FormatUint(math.Float64bits(x), 10))
	case float32:
		b.WriteString(strconv.FormatUint(uint64(math.Float32bits(x)), 10))
	case bool:
		if x {
			b.WriteString("1")
		} else {
			b.WriteString("0")
		}
	case string:
		b.WriteString("s" + hex.EncodeToString([]byte(x)))
	case sliceV:
		b.WriteString(strconv.Itoa(x.n))
		for _, e := range x.elems() {
			if _, nested := e.(sliceV); nested || !encodeToken(b, e) {
				return false
			}
		}
	default:
		return false
	}
	return true
}

// decodeToken is the value of type t, a basic type, that tok writes.
func decodeToken(t check.Type, tok string) (Value, bool) {
	bt, ok := t.(*check.Basic)
	if !ok {
		return nil, false
	}
	switch bt.Kind {
	case check.Bool:
		return tok == "1", tok == "0" || tok == "1"
	case check.String:
		b, err := hex.DecodeString(strings.TrimPrefix(tok, "s"))
		return string(b), err == nil && strings.HasPrefix(tok, "s")
	case check.Float64:
		u, err := strconv.ParseUint(tok, 10, 64)
		return math.Float64frombits(u), err == nil
	case check.Float32:
		u, err := strconv.ParseUint(tok, 10, 32)
		return math.Float32frombits(uint32(u)), err == nil
	case check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
		u, err := strconv.ParseUint(tok, 10, 64)
		if err != nil {
			return nil, false
		}
		return fromUint64(bt.Kind, u)
	}
	i, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return nil, false
	}
	return fromInt64(bt.Kind, i)
}
