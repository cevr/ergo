package lower

import "testing"

// jsonAllocSource is a package registry's index: 200 packages of strings,
// numbers, lists of records, a Map, a Map of Maps and a union, about 90 KB
// of JSON text, larger than one buffer the pool kept before X3 (64 KiB).
const jsonAllocSource = `import json

type Person { Name string, Email string }
type Release { Version string, Size int, Yanked bool }
type State = Active{} | Retired{Reason string}
type Package {
	Name string,
	Summary string,
	Downloads int,
	Score float64,
	Tags []string,
	Maintainers []Person,
	Releases []Release,
	Builds Map[string, int],
	Mirrors Map[string, Map[string, int]],
	State State,
}
type Index { Packages []Package }

func sample() Index {
	let mut ps = []Package{}
	for i in 0..200 {
		ps = ps.Append(Package{
			Name: "pkg-\(i)",
			Summary: "A small library number \(i) that does one \"thing\" well",
			Downloads: i * 37,
			Score: float64(i) / 7.0,
			Tags: []string{"util", "text", "tag-\(i % 13)"},
			Maintainers: []Person{Person{Name: "Dev \(i)", Email: "dev\(i)@example.org"}},
			Releases: []Release{Release{Version: "1.0.\(i)", Size: 1000 + i, Yanked: false}, Release{Version: "1.1.0", Size: 2000 + i, Yanked: i % 5 == 0}},
			Builds: Map[string, int]{"linux": i, "darwin": i / 2, "windows": i / 3},
			Mirrors: Map[string, Map[string, int]]{"west": Map[string, int]{"b": i, "a": 1}, "east": Map[string, int]{"z": 2, "c": i % 4}},
			State: if i % 7 == 0 { Retired{Reason: "moved to pkg-\(i + 1)"} } else { Active },
		})
	}
	Index{Packages: ps}
}

func encodeIndex(idx Index) string { json.Encode(json.Codec[Index]{MaxBytes: 0, MaxDepth: 0}, idx) }

func viaWriter(idx Index) string {
	let t = json.NewText()
	let w Writer = t
	encode(idx, w)
	t.Written()
}

func main() needs Console {
	Console.Println("\(len(encodeIndex(sample())))")
}
`

// jsonAllocDriver encodes the index from Go beside the emitted code and
// counts the heap allocations of an encode after the first: the result
// string, and nothing else.
const jsonAllocDriver = `package main

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
)

var (
	env  = &Env{task: rt.Root("driver")}
	sink string
)

func TestEncodeAllocatesOnlyItsText(t *testing.T) {
	idx := sample()
	text := idx.encodeIndex(env)
	if text != idx.viaWriter(env) {
		t.Fatal("the fast path's text is not the Writer path's")
	}
	if n := testing.AllocsPerRun(20, func() { sink = idx.encodeIndex(env) }); n != 1 {
		t.Errorf("an encode of %d bytes allocates %v times; want 1, its result", len(text), n)
	}
	if sink != text {
		t.Error("an encode again wrote another text")
	}
}
`

// TestJSONEncodeAllocatesOnlyItsText is the X3 acceptance check: std
// json's Encode of a 90 KB value, done again, allocates its result string
// and nothing else, as a hand Go encoder over a pooled buffer does. Before
// X3 the pool dropped a buffer past 64 KiB, so each encode grew a new one
// by appends, and each Map's order was a new slice of its keys.
//
// It builds Go because it counts the allocations of the emitted code at
// run time; no in-process check observes the heap.
func TestJSONEncodeAllocatesOnlyItsText(t *testing.T) {
	needsBuild(t)
	goTestEmitted(t, mustLower(t, "jsonalloc", jsonAllocSource), jsonAllocDriver)
}
