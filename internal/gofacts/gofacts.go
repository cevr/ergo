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
	"slices"
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
	// other operands drawn from r.
	Call func(r *rand.Rand, p []byte)
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
	{Target: "crypto/sha256.Sum256", Fact: "reads", Param: "data", Reason: "Write copies data into the digest's buffer and the block function, assembly, only loads from it", Source: "d1bd973fda38", Call: func(_ *rand.Rand, p []byte) { sha256.Sum256(p) }},
	{Target: "hash/crc32.ChecksumIEEE", Fact: "reads", Param: "data", Reason: "it calls the function variable updateIEEE, assembly or a table loop that only loads", Source: "0a577c2fb9b4", Call: func(_ *rand.Rand, p []byte) { crc32.ChecksumIEEE(p) }},
	{Target: "hash/crc32.Update", Fact: "reads", Param: "p", Reason: "it calls update, over the function variables updateIEEE and updateCastagnoli or a table loop, that only loads", Source: "fadaf3baa7c7", Call: func(_ *rand.Rand, p []byte) { crc32.Update(0, crc32.IEEETable, p) }},
}

// Digest is the digest of the row's ledger line at toolchain (its shown
// key, "go1.27.4"): the fact, the reviewed source hash, the reason and the
// toolchain. A line whose digest differs is stale, and the fact acts only
// once ergo test (go run ./scripts/stdlaws for std) runs the guard again.
func (r Row) Digest(toolchain string) string {
	sum := sha256.Sum256([]byte("fact " + r.Key() + "\nsource " + r.Source + "\nreason " + r.Reason + "\ngo " + toolchain + "\n"))
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
	if r.Fact != "reads" {
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

// draw is n bytes from r, over a small alphabet so a search finds
// something.
func draw(r *rand.Rand, n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = "ab\x00\xff"[r.IntN(4)]
	}
	return p
}
