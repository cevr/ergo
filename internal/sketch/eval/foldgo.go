package eval

import (
	"go/constant"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// The check-time natives of reviewed-pure Go functions (LAWFFI LF3c,
// plan-r1 §2 `pure`: "a call on constants folds at check time"): the
// evaluator calls the same Go function the program would, on the
// constants of a call site. Each takes and gives basic values only.
//
// The value is the compiler's Go's: strconv.Quote and the case mappings
// read Go's Unicode tables, so the lowering folds only when the compiler
// runs on the pinned release (lower/ir_fold.go), the one that builds the
// program.
var goFolds = sync.OnceValue(func() map[string]func(args []Value) (Value, bool) {
	return map[string]func(args []Value) (Value, bool){
		"strconv.Quote":        str1(strconv.Quote),
		"strconv.QuoteToASCII": str1(strconv.QuoteToASCII),
		"strconv.Itoa": func(args []Value) (Value, bool) {
			i, ok := args[0].(int64)
			return strconv.Itoa(int(i)), ok
		},
		"strconv.FormatBool": func(args []Value) (Value, bool) {
			v, ok := args[0].(bool)
			return strconv.FormatBool(v), ok
		},
		"strings.ToUpper":     str1(strings.ToUpper),
		"strings.ToLower":     str1(strings.ToLower),
		"strings.TrimSpace":   str1(strings.TrimSpace),
		"path/filepath.Clean": str1(filepath.Clean),
	}
})

func str1(f func(string) string) func([]Value) (Value, bool) {
	return func(args []Value) (Value, bool) {
		s, ok := args[0].(string)
		return f(s), ok
	}
}

// HasGoFold reports a Go function, by import path and name, that has a
// check-time native.
func HasGoFold(pkg, name string) bool { return goFolds()[pkg+"."+name] != nil }

// FoldGo is the call of the Go function pkg.name on the constants args,
// computed by its check-time native, or nil and the reason it is not.
func FoldGo(pkg, name string, args []constant.Value) (constant.Value, string) {
	f := goFolds()[pkg+"."+name]
	if f == nil {
		return nil, "no check-time native of " + pkg + "." + name
	}
	vals := make([]Value, len(args))
	for i, a := range args {
		switch a.Kind() {
		case constant.String:
			vals[i] = constant.StringVal(a)
		case constant.Bool:
			vals[i] = constant.BoolVal(a)
		case constant.Int:
			n, exact := constant.Int64Val(a)
			if !exact {
				return nil, "an operand is out of int64's range"
			}
			vals[i] = n
		default:
			return nil, "an operand is not a string, a bool or an integer"
		}
	}
	if len(vals) != 1 {
		return nil, "the native takes one operand"
	}
	got, ok := f(vals)
	if !ok {
		return nil, "an operand is not of the native's type"
	}
	return toConstant(got), ""
}
