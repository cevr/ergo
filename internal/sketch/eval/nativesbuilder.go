package eval

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std strings' Builder and byte conversions (strings/builder.ergo,
// strings/bytes.ergo) at check time: a []byte is a slice of uint8 values,
// and each extern does on it what its spec says Go does (append, a
// string conversion), on a copy, since an Ergo slice is a value. The
// self-host packages and the examples build their text with them (wf10
// EVAL-BUILDER; TPROBE's ergo-check forms skipped at builderAppend).
func registerNativesBuilder() {
	bytes := func(name string, f func(in []byte, a []Value) ([]byte, error)) {
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			buf := byteSlice(args[0])
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			out, err := f(buf, args[1:])
			if err != nil {
				return nil, fr.defect(at, "%s", err)
			}
			if ab := in.allocate(fr, at, int64(len(out))); ab != nil {
				return nil, ab
			}
			return bytesValue(string(out)), nil
		}
	}
	bytes("strings.builderAppend", func(b []byte, a []Value) ([]byte, error) { return append(b, a[0].(string)...), nil })
	bytes("strings.builderAppendByte", func(b []byte, a []Value) ([]byte, error) { return append(b, a[0].(uint8)), nil })
	bytes("strings.builderAppendBytes", func(b []byte, a []Value) ([]byte, error) { return append(b, byteSlice(a[0])...), nil })
	bytes("strings.builderGrow", func(b []byte, a []Value) ([]byte, error) {
		if a[0].(int64) < 0 {
			return nil, fmt.Errorf("strings.Builder.Grow: negative count")
		}
		return b, nil
	})
	bytes("strings.builderAppendFrom", func(b []byte, a []Value) ([]byte, error) {
		start, n := a[0].(int64), a[1].(int64)
		switch {
		case n < 0 || start < 0:
			return nil, fmt.Errorf("slice bounds out of range [%d:%d]", start, start+n)
		case start+n <= int64(len(b)):
			return append(b, b[start:start+n]...), nil
		case start >= int64(len(b)):
			return nil, fmt.Errorf("index out of range [%d] with length %d", start, len(b))
		}
		// Byte k of the n is the byte at start+k of the result: a run
		// past the end repeats what this call wrote.
		for k := int64(0); k < n; k++ {
			b = append(b, b[start+k])
		}
		return b, nil
	})
	text := func(name string) {
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			out := string(byteSlice(args[0]))
			return out, in.allocate(fr, at, int64(len(out)))
		}
	}
	text("strings.builderText")
	natives["strings.stringsBytes"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		s := args[0].(string)
		if ab := in.charge(fr, at, 1+int64(len(s))); ab != nil {
			return nil, ab
		}
		return bytesValue(s), in.allocate(fr, at, int64(len(s)))
	}
}

// byteSlice is the bytes of a []byte value, a copy.
func byteSlice(v Value) []byte {
	es := v.(sliceV).elems()
	out := make([]byte, len(es))
	for k, e := range es {
		out[k] = e.(uint8)
	}
	return out
}
