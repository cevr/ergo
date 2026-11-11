package lower

import "testing"

// jsonFieldsBenchSource is the program of BenchmarkJSONFields: a page of
// items whose json.Optional fields are absent in half the items and whose
// json.Nullable fields are null in a third, so the omission and null
// branches both run. encoding/json writes the same bytes from hand structs
// with pointer fields: omitempty for Optional, a plain pointer for Nullable.
const jsonFieldsBenchSource = `import json

type Item { Name string, Rank json.Optional[int], Note json.Nullable[string], Owner json.Optional[string] }
type Page { Items []Item, Next json.Optional[string], Prev json.Nullable[string] }

func rank(i int) json.Optional[int] {
	if i % 2 == 0 {
		return json.Present{Value: i * 1000}
	}
	json.Absent
}

func note(i int) json.Nullable[string] {
	if i % 3 == 0 {
		return json.Null
	}
	json.NonNull{Value: "note \"\(i)\""}
}

func owner(i int) json.Optional[string] {
	if i % 2 == 1 {
		return json.Present{Value: "team-\(i)"}
	}
	json.Absent
}

func sample() Page {
	let mut items = []Item{}
	for i in 0..16 {
		items = items.Append(Item{Name: "item \(i)", Rank: rank(i), Note: note(i), Owner: owner(i)})
	}
	Page{Items: items, Next: json.Present{Value: "cursor-17"}, Prev: json.Null}
}

func codec() json.Codec[Page] { json.Codec[Page]{MaxBytes: 0, MaxDepth: 0} }

func fast(p Page) string { json.Encode(codec(), p) }

func viaWriter(p Page) string {
	let t = json.NewText()
	let w Writer = t
	encode(p, w)
	t.Written()
}

func back(s string) Page {
	json.Decode(codec(), s) catch {
	case _:
		sample()
	}
}

func main() needs Console {
	Console.Println(fast(sample()))
}
`

// jsonFieldsBenchGo is the Go benchmark file beside the emitted program.
func jsonFieldsBenchGo(string) string {
	tag := func(s string) string { return "`json:\"" + s + "\"`" }
	return `package main

import (
	"encoding/json"
	"testing"
)

type handItem struct {
	Name  string  ` + tag("name") + `
	Rank  *int    ` + tag("rank,omitempty") + `
	Note  *string ` + tag("note") + `
	Owner *string ` + tag("owner,omitempty") + `
}

type handPage struct {
	Items []handItem ` + tag("items") + `
	Next  *string    ` + tag("next,omitempty") + `
	Prev  *string    ` + tag("prev") + `
}

func handSample(b *testing.B) (handPage, string) {
	text := sample().fast(benchEnv)
	var h handPage
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
	p := sample()
	handSample(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = p.fast(benchEnv)
	}
}

func BenchmarkErgoEncodeWriter(b *testing.B) {
	p := sample()
	if p.viaWriter(benchEnv) != p.fast(benchEnv) {
		b.Fatal("the paths differ")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = p.viaWriter(benchEnv)
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
		var h handPage
		_ = json.Unmarshal(data, &h)
	}
}
`
}

// BenchmarkJSONFields measures one page of optional and nullable fields
// (ns/op is per page) written and read by std json, the fast path and the
// reference Writer path, against encoding/json over hand structs with
// pointer fields. Interleave the runs (-count) on a quiet host for a ratio;
// one run on a loaded host is no claim (bench-pending.md).
func BenchmarkJSONFields(b *testing.B) {
	emittedBench(b, "jsonfieldsbench", jsonFieldsBenchSource, jsonFieldsBenchGo, "ErgoEncode", "GoEncode", "ErgoEncodeWriter", "ErgoDecode", "GoDecode")
}
