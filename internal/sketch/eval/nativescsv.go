package eval

import (
	"encoding/csv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The std csv functions (ORACLE O25), from Go's encoding/csv as the std's
// doc comments name it: its defaults (a comma, RFC 4180 quotes, the first
// record's field count held for every other), ReadAll's error as the
// ParseError record, scan as a loop over Reader.Read with FieldPos, and
// Format as csv.Writer with a row of one empty field written "".
func init() {
	parseError := func(fn *check.Func, err error) *record {
		if pe, ok := err.(*csv.ParseError); ok {
			return stdRecord(fn, "ParseError", map[string]Value{"Line": int64(pe.Line), "Column": int64(pe.Column), "Reason": pe.Err.Error(), "Message": pe.Error()})
		}
		return stdRecord(fn, "ParseError", map[string]Value{"Line": int64(0), "Column": int64(0), "Reason": err.Error(), "Message": err.Error()})
	}
	natives["csv.ReadAll"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		text := args[0].(string)
		if ab := in.charge(fr, at, 1+int64(len(text))); ab != nil {
			return nil, ab
		}
		rows, err := csv.NewReader(strings.NewReader(text)).ReadAll()
		if err != nil {
			return nil, fr.abrupt(ctlFail, at, parseError(fn, err), "")
		}
		out := make([]Value, len(rows))
		for i, r := range rows {
			out[i] = stringSlice(r)
		}
		v := newSlice(out)
		if ab := in.allocate(fr, at, valueSize(v)); ab != nil {
			return nil, ab
		}
		return v, nil
	}
	natives["csv.Format"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
			return nil, ab
		}
		var b strings.Builder
		w := csv.NewWriter(&b)
		for _, row := range args[0].(sliceV).elems() {
			var rec []string
			for _, f := range row.(sliceV).elems() {
				rec = append(rec, f.(string))
			}
			if len(rec) == 1 && rec[0] == "" {
				w.Flush()
				b.WriteString("\"\"\n")
				continue
			}
			_ = w.Write(rec)
		}
		w.Flush()
		if ab := in.allocate(fr, at, int64(b.Len())); ab != nil {
			return nil, ab
		}
		return b.String(), nil
	}
	natives["csv.scan"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		text := args[0].(string)
		none := stdRecord(fn, "ParseError", map[string]Value{"Line": int64(0), "Column": int64(0), "Reason": "", "Message": ""})
		rd := csv.NewReader(strings.NewReader(text))
		for {
			before := rd.InputOffset()
			rec, err := rd.Read()
			if ab := in.charge(fr, at, 1+rd.InputOffset()-before); ab != nil {
				return nil, ab
			}
			if err != nil {
				if err.Error() == "EOF" {
					return none, nil
				}
				return parseError(fn, err), nil
			}
			pos := make([]Value, len(rec))
			for i := range rec {
				line, col := rd.FieldPos(i)
				pos[i] = stdRecord(fn, "Position", map[string]Value{"Line": int64(line), "Column": int64(col)})
			}
			fields, positions := stringSlice(rec), newSlice(pos)
			if ab := in.allocate(fr, at, valueSize(fields)+valueSize(positions)); ab != nil {
				return nil, ab
			}
			more, ab := in.callValue(fr, at, args[1], []Value{fields, positions})
			if ab != nil {
				return nil, ab
			}
			if !more.(bool) {
				return none, nil
			}
		}
	}
}
