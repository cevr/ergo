package lower

import "ergo.local/prototype/internal/sketch/check"

// Float to integer conversion (types.md, Conversions 4). Go leaves T(x) of
// a float x that the integer type T cannot hold to the implementation
// (linux/amd64 gives the most negative value, arm64 saturates), so the
// lowering never writes that Go conversion: it calls the runtime/v1
// function for T, which truncates toward zero, saturates at T's bounds and
// gives 0 for NaN on every host. The function guards one Go conversion
// with two compares and inlines (TestFloatConversionInlines); a float32
// widens to float64 first, which is exact.

// floatToIntegerHelpers names the runtime/v1 function of each integer
// kind; byte and rune are Uint8 and Int32.
var floatToIntegerHelpers = map[check.BasicKind]string{
	check.Int:     "FloatToInt",
	check.Int8:    "FloatToInt8",
	check.Int16:   "FloatToInt16",
	check.Int32:   "FloatToInt32",
	check.Int64:   "FloatToInt64",
	check.Uint:    "FloatToUint",
	check.Uint8:   "FloatToUint8",
	check.Uint16:  "FloatToUint16",
	check.Uint32:  "FloatToUint32",
	check.Uint64:  "FloatToUint64",
	check.Uintptr: "FloatToUintptr",
}

// floatToInteger returns the runtime/v1 function that converts a value of
// type from to type to, or "" when the conversion is not of a float to an
// integer.
func floatToInteger(from, to check.Type) string {
	f, ok := from.(*check.Basic)
	if !ok || f.Kind != check.Float32 && f.Kind != check.Float64 {
		return ""
	}
	t, ok := to.(*check.Basic)
	if !ok {
		return ""
	}
	return floatToIntegerHelpers[t.Kind]
}

// float64Of is val, of the float type from, as a float64.
func float64Of(from check.Type, val string) string {
	if from.(*check.Basic).Kind == check.Float32 {
		return "float64(" + val + ")"
	}
	return val
}
