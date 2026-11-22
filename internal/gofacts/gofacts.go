// Package gofacts is the table of vouched foreign facts (LAWFFI LF2;
// docs/spec/interop.md, Foreign facts 7): what a reviewer says a Go
// function of the standard library does with an operand where Go's own
// escape analysis cannot see it (an assembly body, a function variable).
// A row holds the function, the fact, the binding's binder, the reason and
// the hash of the function's Go declaration at the pinned toolchain
// (SourceHash): a row whose function changed is reviewed again
// (TestVouchedRowsReviewed fails until it is).
//
// A row changes no code by itself: its level is vouched. Its guard, run
// by the std ledger's fresh run (go run ./scripts/stdlaws), writes a fact
// line in the shipped ledger, `fact go bytes.IndexByte reads b validated
// by guard with go1.27.4 h1:…`; the lowering admits the fact only on a
// current validated line (lower/factledger.go), and a current refuted line
// stops every build that reaches the fact.
//
// The table lives in the compiler, not in runtime/v1: a fact is the
// compiler's knowledge, and a guard calls the Go function, which no
// program needs linked.
package gofacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// Row is one vouched fact.
type Row struct {
	// Target is the extern target of the function's binding
	// ("bytes.IndexByte", "hash/crc32.Update").
	Target string
	// Fact is the fact's word ("reads"); Param the binding's binder.
	Fact, Param string
	// Reason says why Go's analysis cannot derive the fact, and why the
	// reviewer holds it.
	Reason string
	// Source is SourceHash of the function's declaration at the pinned
	// toolchain.
	Source string
	// Call calls the function with p as the operand Param names, the
	// other operands drawn from r (a reads row).
	Call func(r *rand.Rand, p []byte)
	// CallStrings is Call for a []string operand (a reads row of a
	// function of string slices).
	CallStrings func(r *rand.Rand, p []string)
	// Result draws the operands of one call from r and returns the call,
	// whose result is a []string or a [][]string (a fresh row, LF2c).
	Result func(r *rand.Rand) func() any
	// View allows the run build to pass a view of a string's bytes as the
	// operand (LAWFFI LF2b; v1.BytesView): a memory-class change, so a row
	// sets it only with a counted gain, and each such row has its line in
	// internal/residue/testdata/unsafe.txt (TestUnsafeBodiesListed).
	View bool
}

// Key is the row's fact as the ledger names it: `go <target> <fact>
// <param>`.
func (r Row) Key() string { return "go " + r.Target + " " + r.Fact + " " + r.Param }

// Rows is the table. Each row's gain is counted in LF2's request (a row
// with no gain is not a row).
var Rows = []Row{
	{Target: "bytes.IndexByte", Fact: "reads", Param: "b", Reason: "its body is bytealg.IndexByte, assembly that only loads from b", Source: "9cb4c150b9d9", Call: func(r *rand.Rand, p []byte) { bytes.IndexByte(p, byte(r.IntN(256))) }},
	{Target: "bytes.Index", Fact: "reads", Param: "s", Reason: "it reaches bytealg.Index, IndexByte and Equal, assembly that only loads, and a Rabin-Karp loop that only reads", Source: "711d86f96509", Call: func(r *rand.Rand, p []byte) { bytes.Index(p, draw(r, r.IntN(4))) }},
	{Target: "bytes.Index", Fact: "reads", Param: "sep", Reason: "it reaches bytealg.Index, IndexByte and Equal, assembly that only loads, and a Rabin-Karp loop that only reads", Source: "711d86f96509", Call: func(r *rand.Rand, p []byte) { bytes.Index(draw(r, r.IntN(64)), p) }},
	{Target: "bytes.Contains", Fact: "reads", Param: "b", Reason: "it is Index(b, subslice) != -1", Source: "cf60e3ee8bcf", Call: func(r *rand.Rand, p []byte) { bytes.Contains(p, draw(r, r.IntN(4))) }},
	{Target: "bytes.Contains", Fact: "reads", Param: "subslice", Reason: "it is Index(b, subslice) != -1", Source: "cf60e3ee8bcf", Call: func(r *rand.Rand, p []byte) { bytes.Contains(draw(r, r.IntN(64)), p) }},
	{Target: "bytes.Count", Fact: "reads", Param: "s", Reason: "it reaches bytealg.Count (assembly that only loads), utf8.RuneCount and Index", Source: "28a1ec4078dd", Call: func(r *rand.Rand, p []byte) { bytes.Count(p, draw(r, r.IntN(3))) }},
	{Target: "bytes.Count", Fact: "reads", Param: "sep", Reason: "it reads sep[0] for bytealg.Count, and passes sep to Index", Source: "28a1ec4078dd", Call: func(r *rand.Rand, p []byte) { bytes.Count(draw(r, r.IntN(64)), p) }},
	{Target: "crypto/sha256.Sum256", Fact: "reads", Param: "data", Reason: "Write copies data into the digest's buffer and the block function, assembly, only loads from it", Source: "d1bd973fda38", Call: func(_ *rand.Rand, p []byte) { sha256.Sum256(p) }, View: true},
	{Target: "hash/crc32.ChecksumIEEE", Fact: "reads", Param: "data", Reason: "it calls the function variable updateIEEE, assembly or a table loop that only loads", Source: "0a577c2fb9b4", Call: func(_ *rand.Rand, p []byte) { crc32.ChecksumIEEE(p) }, View: true},
	// Reads rows of []string operands (JOIN, from NP4f-4): Go derives no
	// fact, so the value rule copied the slice before each call.
	{Target: "strings.Join", Fact: "reads", Param: "elems", Reason: "Go reports only that an element string flows to the result (leaking param: elems to result level=1): the slice's array is neither written nor kept, and a string is immutable", Source: "8aa66715ab45", CallStrings: func(r *rand.Rand, p []string) { strings.Join(p, seps[r.IntN(len(seps))]) }},
	{Target: "path/filepath.Join", Fact: "reads", Param: "elem", Reason: "it is join(elem), which reslices elem and passes it to strings.Join (a reads row); Go's mutation verdict comes from the inlined Clean of the joined string, which is immutable", Source: "6fc7b1fadbe3", CallStrings: func(_ *rand.Rand, p []string) { filepath.Join(p...) }},
	{Target: "hash/crc32.Update", Fact: "reads", Param: "p", Reason: "it calls update, over the function variables updateIEEE and updateCastagnoli or a table loop, that only loads", Source: "fadaf3baa7c7", Call: func(_ *rand.Rand, p []byte) { crc32.Update(0, crc32.IEEETable, p) }, View: true},

	// fresh rows (LF2c): the slice result is new memory on every call, so
	// the lowering passes it to Ergo with no deep copy (lower/gocall.go,
	// goResult). They were lower.freshGoResults.
	{Target: "strings.Split", Fact: "fresh", Param: "result", Reason: "genSplit makes the result with make and fills it with substrings of s", Source: "65e3a355768c", Result: func(r *rand.Rand) func() any {
		s, sep := text(r), seps[r.IntN(len(seps))]
		return func() any { return strings.Split(s, sep) }
	}},
	{Target: "strings.SplitN", Fact: "fresh", Param: "result", Reason: "genSplit makes the result with make and fills it with substrings of s", Source: "ebc89999b4be", Result: func(r *rand.Rand) func() any {
		s, sep, n := text(r), seps[r.IntN(len(seps))], r.IntN(5)-1
		return func() any { return strings.SplitN(s, sep, n) }
	}},
	{Target: "strings.SplitAfter", Fact: "fresh", Param: "result", Reason: "genSplit makes the result with make and fills it with substrings of s", Source: "3568d9599e11", Result: func(r *rand.Rand) func() any {
		s, sep := text(r), seps[r.IntN(len(seps))]
		return func() any { return strings.SplitAfter(s, sep) }
	}},
	{Target: "strings.Fields", Fact: "fresh", Param: "result", Reason: "it makes the result with make (or calls FieldsFunc) and fills it with substrings of s", Source: "03cc79a67909", Result: func(r *rand.Rand) func() any {
		s := text(r)
		return func() any { return strings.Fields(s) }
	}},
	{Target: "strings.FieldsFunc", Fact: "fresh", Param: "result", Reason: "it makes spans and the result with make and fills the result with substrings of s", Source: "e7015b80f1e8", Result: func(r *rand.Rand) func() any {
		s := text(r)
		return func() any { return strings.FieldsFunc(s, func(c rune) bool { return c == ',' }) }
	}},
	{Target: "regexp.Regexp.FindAllString", Fact: "fresh", Param: "result", Reason: "it is slices.Collect over allString, which appends each match's substring to a nil slice", Source: "e0b9bb9db7ad", Result: func(r *rand.Rand) func() any {
		s, n := text(r), r.IntN(4)-1
		return func() any { return freshRE().FindAllString(s, n) }
	}},
	{Target: "regexp.Regexp.FindStringSubmatch", Fact: "fresh", Param: "result", Reason: "it makes the result with make from the match's indexes", Source: "d6982e2c79d9", Result: func(r *rand.Rand) func() any {
		s := text(r)
		return func() any { return freshRE().FindStringSubmatch(s) }
	}},
	{Target: "regexp.Regexp.FindAllStringSubmatch", Fact: "fresh", Param: "result", Reason: "it is slices.Collect over allStringSubmatch, which makes each match's slice with make", Source: "670036968b42", Result: func(r *rand.Rand) func() any {
		s, n := text(r), r.IntN(4)-1
		return func() any { return freshRE().FindAllStringSubmatch(s, n) }
	}},
	{Target: "regexp.Regexp.Split", Fact: "fresh", Param: "result", Reason: "it makes the result with make and appends substrings of s", Source: "f9bf7fc8970d", Result: func(r *rand.Rand) func() any {
		s, n := text(r), r.IntN(4)-1
		return func() any { return freshRE().Split(s, n) }
	}},
}

// freshRE is the receiver of the regexp rows' guards: a group, so a
// submatch has two strings.
var freshRE = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`a(b*)`) })

// seps are the separators the fresh guards split on.
var seps = []string{"", ",", "ab", " "}

// text is a drawn string for a fresh guard, over an alphabet the
// separators and freshRE find.
func text(r *rand.Rand) string {
	var b strings.Builder
	for range r.IntN(24) {
		b.WriteByte("ab, \t"[r.IntN(5)])
	}
	return b.String()
}

// Digest is the digest of the row's ledger line at toolchain (its shown
// key, "go1.27.4"): the fact, the reviewed source hash, the reason,
// whether it allows a view, and the toolchain. A line whose digest differs is stale, and the fact acts only
// once ergo test (go run ./scripts/stdlaws for std) runs the guard again.
func (r Row) Digest(toolchain string) string {
	sum := sha256.Sum256([]byte("fact " + r.Key() + "\nsource " + r.Source + "\nreason " + r.Reason + "\nview " + strconv.FormatBool(r.View) + "\ngo " + toolchain + "\n"))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

// GuardSeed is the seed the ledger's guard run uses; a failing guard
// prints its own seed and case.
const GuardSeed = 1

// Of is the rows of target.
func Of(target string) []Row {
	var out []Row
	for _, r := range Rows {
		if r.Target == target {
			out = append(out, r)
		}
	}
	return out
}

// guardLengths are the operand lengths every guard tries first: the
// edges of word, vector and block widths, then a page.
var guardLengths = []int{0, 1, 2, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 255, 256, 257, 4095, 4096, 4097}

// GuardCases is the number of drawn cases a guard runs after the edges.
const GuardCases = 200

// Guard runs the row's guard with seed: each operand of guardLengths,
// then GuardCases drawn ones, is passed to the function, and the guard
// fails at the first one the call left changed (a write the reads fact
// says never happens). A keep with no later write is not seen: review
// holds it (interop.md, Foreign facts 7).
func (r Row) Guard(seed uint64) error {
	if r.Fact == "fresh" && r.Result != nil {
		return r.freshGuard(seed)
	}
	if r.Fact == "reads" && r.CallStrings != nil {
		return r.stringsGuard(seed)
	}
	if r.Fact != "reads" || r.Call == nil {
		return fmt.Errorf("%s: no guard for the fact %s", r.Target, r.Fact)
	}
	rng := rand.New(rand.NewPCG(seed, 0x1f2))
	n := len(guardLengths) + GuardCases
	for i := range n {
		size := 0
		if i < len(guardLengths) {
			size = guardLengths[i]
		} else {
			size = rng.IntN(1 << uint(rng.IntN(13)+1))
		}
		p := draw(rng, size)
		held := slices.Clone(p)
		r.Call(rng, p)
		if !bytes.Equal(held, p) {
			return fmt.Errorf("%s wrote %s at case %d (length %d, seed %d)", r.Target, r.Param, i, size, seed)
		}
	}
	return nil
}

// stringsGuard is the guard of a reads row of a []string operand: each
// length of guardLengths up to 257, then GuardCases drawn ones, and the
// guard fails at the first call that left the slice changed.
func (r Row) stringsGuard(seed uint64) error {
	rng := rand.New(rand.NewPCG(seed, 0x51e))
	for i := range len(guardLengths) + GuardCases {
		size := rng.IntN(1 << uint(rng.IntN(6)+1))
		if i < len(guardLengths) {
			size = min(guardLengths[i], 257)
		}
		p := make([]string, size)
		for j := range p {
			p[j] = []string{"", "a", "/", "..", "b/c", "\x00"}[rng.IntN(6)]
		}
		held := slices.Clone(p)
		r.CallStrings(rng, p)
		if !slices.Equal(held, p) {
			return fmt.Errorf("%s wrote %s at case %d (length %d, seed %d)", r.Target, r.Param, i, size, seed)
		}
	}
	return nil
}

// freshGuard is a fresh row's guard: for GuardCases drawn calls, it calls
// once, writes every string of the result, and calls again: the second
// result must be the first's value before the write. A result that shares
// memory with state Go keeps (regexp.Regexp.SubexpNames) or with the
// first call's shows the write (plan-r1 §1, fresh: mutate after the call,
// call again, compare).
func (r Row) freshGuard(seed uint64) error {
	rng := rand.New(rand.NewPCG(seed, 0xf5e))
	for i := range GuardCases {
		call := r.Result(rng)
		first := stringSlices(call())
		held := make([][]string, len(first))
		for j, s := range first {
			held[j] = slices.Clone(s)
			for k := range s {
				s[k] = "\x00written"
			}
		}
		again := stringSlices(call())
		if !slices.EqualFunc(held, again, slices.Equal) {
			return fmt.Errorf("%s: a write to one call's result shows in the next call's at case %d (seed %d): %q, then %q", r.Target, i, seed, held, again)
		}
	}
	return nil
}

// stringSlices is a fresh row's result as its string slices.
func stringSlices(v any) [][]string {
	switch v := v.(type) {
	case []string:
		return [][]string{v}
	case [][]string:
		return v
	}
	panic(fmt.Sprintf("gofacts: a fresh row's result is %T, not []string or [][]string", v))
}

// draw is n bytes from r, over a small alphabet so a search finds
// something.
func draw(r *rand.Rand, n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = "ab\x00\xff"[r.IntN(4)]
	}
	return p
}
