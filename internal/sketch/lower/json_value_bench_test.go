package lower

import "testing"

// jsonValueBenchSource decodes and encodes one document of about 1 KB as a
// json.Value: nested objects and arrays, numbers with exponents, strings
// with escapes.
const jsonValueBenchSource = `import json

func doc() string {
	let mut items = "["
	for i in 0..16 {
		if i > 0 { items = items + "," }
		items = items + "{\"id\":" + "\(i)" + ",\"name\":\"item \\\"\(i)\\\"\",\"score\":1.5e" + "\(i)" + ",\"tags\":[\"a\",\"b\",null,true]}"
	}
	"{\"kind\":\"page\",\"items\":" + items + "],\"next\":null,\"total\":12345678901234567890}"
}

func codec() json.Codec[json.Value] { json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0} }

func back(s string) json.Value {
	json.Decode(codec(), s) catch {
	case _:
		json.Null
	}
}

func fast(v json.Value) string { json.Encode(codec(), v) }

func main() needs Console {
	Console.Println(fast(back(doc())))
}
`

// jsonValueBenchGo is the Go benchmark file. The control decodes into any
// (map[string]any loses the member order and float64 loses the long
// number, so it does less than Value) and json.RawMessage (keeps the
// bytes and reads no structure, so it does less again).
func jsonValueBenchGo(string) string {
	return `package main

import (
	"encoding/json"
	"testing"
)

func BenchmarkErgoDecode(b *testing.B) {
	text := doc()
	if fast(benchEnv, back(benchEnv, text)) != text {
		b.Fatal("the value does not round-trip")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = back(benchEnv, text)
	}
}

func BenchmarkErgoEncode(b *testing.B) {
	v := back(benchEnv, doc())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = fast(benchEnv, v)
	}
}

func BenchmarkGoDecodeAny(b *testing.B) {
	data := []byte(doc())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var v any
		_ = json.Unmarshal(data, &v)
	}
}

func BenchmarkGoEncodeAny(b *testing.B) {
	var v any
	if err := json.Unmarshal([]byte(doc()), &v); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = json.Marshal(v)
	}
}

func BenchmarkGoDecodeRaw(b *testing.B) {
	data := []byte(doc())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var v json.RawMessage
		_ = json.Unmarshal(data, &v)
	}
}
`
}

// BenchmarkJSONValue measures one 1 KB document decoded to and encoded from
// a json.Value, against encoding/json into any and into json.RawMessage.
// Interleave the runs (-count) on a quiet host for a ratio (bench-pending.md).
func BenchmarkJSONValue(b *testing.B) {
	emittedBench(b, "jsonvaluebench", jsonValueBenchSource, jsonValueBenchGo, "ErgoDecode", "GoDecodeAny", "GoDecodeRaw", "ErgoEncode", "GoEncodeAny")
}
