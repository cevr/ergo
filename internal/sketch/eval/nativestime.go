package eval

import (
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The std time text functions (ORACLE O13), from Go's time package as the
// std doc comments state them: format is Go's t.UTC().Format(layout) of
// the instant (sec, nsec); parse is Go's time.ParseInLocation(layout,
// value, time.UTC) as a Reading with no monotonic part, an error the
// ParseError with Go's message. Neither reads the host's zone.
func init() {
	natives["time.format"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		out := time.Unix(args[0].(int64), args[1].(int64)).UTC().Format(args[2].(string))
		if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
			return nil, ab
		}
		return out, nil
	}
	natives["time.parse"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		layout, value := args[0].(string), args[1].(string)
		t, err := time.ParseInLocation(layout, value, time.UTC)
		if err != nil {
			return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "ParseError", map[string]Value{"Layout": layout, "Value": value, "Message": err.Error()}), "")
		}
		return stdRecord(fn, "Reading", map[string]Value{"Sec": t.Unix(), "Nsec": int64(t.Nanosecond()), "Mono": int64(0)}), nil
	}
}
