package lower

import (
	"os"
	"strings"
	"testing"
)

// jsonBenchSource is the program of BenchmarkJSON: a deploy record of
// strings, integers, floats, a bool, a slice of records and a Map, which
// encoding/json writes with the same keys (jsonBenchGo's hand structs), so
// the texts are equal byte for byte and the runs do the same work.
const jsonBenchSource = `import json

type Check { Name string, Passed bool, Millis int64 }
type Deploy { ID string, Service string, Version string, Replicas int, Load float64, Ready bool, Checks []Check, Labels Map[string, string], Notes []string }

func sample() Deploy {
	let mut checks = []Check{}
	for i in 0..16 {
		checks = checks.Append(Check{Name: "probe \"\(i)\"", Passed: i % 3 != 0, Millis: int64(i * 1234567)})
	}
	Deploy{ID: "d-0001", Service: "billing-api", Version: "v2.14.3", Replicas: 12, Load: 0.734, Ready: true, Checks: checks, Labels: Map[string, string]{"team": "payments", "tier": "gold", "region": "eu-west-1"}, Notes: []string{"rolled\tout", "canary ok", "élan"}}
}

func codec() json.Codec[Deploy] { json.Codec[Deploy]{MaxBytes: 0, MaxDepth: 0} }

func fast(d Deploy) string { json.Encode(codec(), d) }

func viaWriter(d Deploy) string {
	let t = json.NewText()
	let w Writer = t
	encode(d, w)
	t.Written()
}

func back(s string) Deploy {
	json.Decode(codec(), s) catch {
	case _:
		sample()
	}
}

func main() needs Console {
	Console.Println(fast(sample()))
}
`

// jsonBenchGo is the Go benchmark file beside the emitted program: the
// Ergo encodes (the fast path and the Writer path) and decode, and the
// encoding/json controls over hand structs with the same field keys. A
// Map's keys are written in order by both. v2 adds encoding/json/v2's
// Marshal and Unmarshal (GOEXPERIMENT=jsonv2 builds).
func jsonBenchGo(v2 bool) func(string) string {
	return func(string) string {
		imp := `"encoding/json"`
		if v2 {
			imp = `json "encoding/json/v2"`
		}
		src := `package main

import (
	` + imp + `
	"testing"
)

type handCheck struct {
	Name   string ` + "`json:\"name\"`" + `
	Passed bool   ` + "`json:\"passed\"`" + `
	Millis int64  ` + "`json:\"millis\"`" + `
}

type handDeploy struct {
	ID       string            ` + "`json:\"iD\"`" + `
	Service  string            ` + "`json:\"service\"`" + `
	Version  string            ` + "`json:\"version\"`" + `
	Replicas int               ` + "`json:\"replicas\"`" + `
	Load     float64           ` + "`json:\"load\"`" + `
	Ready    bool              ` + "`json:\"ready\"`" + `
	Checks   []handCheck       ` + "`json:\"checks\"`" + `
	Labels   map[string]string ` + "`json:\"labels\"`" + `
	Notes    []string          ` + "`json:\"notes\"`" + `
}

func handSample(b *testing.B) (handDeploy, string) {
	text := sample().fast(benchEnv)
	var h handDeploy
	if err := json.Unmarshal([]byte(text), &h); err != nil {
		b.Fatal(err)
	}
	out, err := json.Marshal(h)
	if err != nil {
		b.Fatal(err)
	}
	if string(out) != text {
		b.Fatalf("the texts differ:\n%s\n%s", out, text)
	}
	return h, text
}

func BenchmarkErgoEncode(b *testing.B) {
	d := sample()
	handSample(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = d.fast(benchEnv)
	}
}

func BenchmarkErgoEncodeWriter(b *testing.B) {
	d := sample()
	if d.viaWriter(benchEnv) != d.fast(benchEnv) {
		b.Fatal("the paths differ")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = d.viaWriter(benchEnv)
	}
}

func BenchmarkErgoDecode(b *testing.B) {
	text := sample().fast(benchEnv)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = back(benchEnv, text)
	}
}

func BenchmarkGoEncode(b *testing.B) {
	h, _ := handSample(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = json.Marshal(h)
	}
}

func BenchmarkGoDecode(b *testing.B) {
	_, text := handSample(b)
	data := []byte(text)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var h handDeploy
		_ = json.Unmarshal(data, &h)
	}
}
`
		if v2 {
			src = strings.ReplaceAll(src, "BenchmarkGo", "BenchmarkGoV2")
			src = strings.ReplaceAll(src, "json.Marshal(h)", "json.Marshal(h, json.Deterministic(true))")
		}
		return src
	}
}

// BenchmarkJSON measures one deploy record (about 1 KB of text; ns/op
// is per record) written and read by std json, the fast path (ErgoEncode)
// and the reference Writer path (ErgoEncodeWriter), against Go's
// encoding/json over hand structs (GoEncode, GoDecode), and, in a second
// build with GOEXPERIMENT=jsonv2, encoding/json/v2. Interleave the runs
// (-count with the sub-benchmarks alternating) on a quiet host for a
// ratio; one run on a loaded host is no claim (bench-pending.md).
func BenchmarkJSON(b *testing.B) {
	emittedBench(b, "jsonbench", jsonBenchSource, jsonBenchGo(false), "ErgoEncode", "GoEncode", "ErgoEncodeWriter", "ErgoDecode", "GoDecode")
	exp := os.Getenv("GOEXPERIMENT")
	if !strings.Contains(exp, "jsonv2") {
		b.Setenv("GOEXPERIMENT", strings.TrimPrefix(exp+",jsonv2", ","))
	}
	emittedBench(b, "jsonbenchv2", jsonBenchSource, jsonBenchGo(true), "GoV2Encode", "GoV2Decode")
}
