package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The std time text functions (ORACLE O13) write and read the text the
// build does (timetext.go): outside the years 0 to 9999 both write and
// read ISO 8601's expanded year, where Go's own Format writes a year its
// Parse refuses, so a law over Format or Parse run at check time judges
// what the program does. Neither reads the host's zone.
func registerNativestime(t nativeTables) {
	natives := t.std
	t.reads["time.format"] = "int64 int64 string"
	natives["time.format"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		out := formatTime(args[0].(int64), args[1].(int64), args[2].(string))
		if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
			return nil, ab
		}
		return out, nil
	}
	t.reads["time.parse"] = "string string"
	natives["time.parse"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		layout, value := args[0].(string), args[1].(string)
		r, pe := parseTime(layout, value)
		if pe != nil {
			return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "ParseError", map[string]Value{"Layout": pe.Layout, "Value": pe.Value, "Message": pe.Message}), "")
		}
		return stdRecord(fn, "Reading", map[string]Value{"Sec": r.Sec, "Nsec": r.Nsec, "Mono": int64(0)}), nil
	}
}
