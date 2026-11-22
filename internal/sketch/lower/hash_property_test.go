package lower

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// hashProgram is seed's program for TestHashThroughProviderAgrees: a
// provider Mod of Hashing[int] whose Equal compares keys by their residue
// and whose Hash hashes the residue with hash, a Map keyed through it
// counting seeded keys, and the test V asserting that equal keys hash
// equal and that each key's count is the model's: the number of keys of
// its residue. Hash values differ by process (one maphash seed each), so
// nothing compares a hash but to another of the same run.
func hashProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	mod := 2 + r.IntN(8)
	n := 4 + r.IntN(12)
	keys := make([]int, n)
	count := map[int]int{}
	for i := range keys {
		keys[i] = r.IntN(61) - 30
		count[((keys[i]%mod)+mod)%mod]++
	}
	texts := make([]string, n)
	for i, k := range keys {
		texts[i] = strconv.Itoa(k)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `type Mod {} implements Hashing[int]

func rem(k int) int { ((k %% %d) + %d) %% %d }

func Hash(_ Mod, k int) uint64 { hash(rem(k)) }

func Equal(_ Mod, a int, b int) bool { rem(a) == rem(b) }

test V {
	let p = Mod
	let keys = []int{%s}
	for a in keys {
		for c in keys {
			assert !p.Equal(a, c) || p.Hash(a) == p.Hash(c)
			assert a != c || hash(a) == hash(c)
			assert a != c || hash("k\(a)") == hash("k\(c)")
		}
	}
	let mut m = Map[int, int, Mod]{}
	for k in keys {
		let n = match m.Get(k) {
		case Some[int] as c:
			c.Value + 1
		case None:
			1
		}
		m[k] = n
	}
	assert len(m) == %d
`, mod, mod, mod, strings.Join(texts, ", "), len(count))
	for _, k := range keys {
		fmt.Fprintf(&b, `	match m.Get(%d) {
	case Some[int] as c:
		assert c.Value == %d
	case None:
		assert false
	}
`, k, count[((k%mod)+mod)%mod])
	}
	b.WriteString("}\n")
	return b.String()
}

// TestHashThroughProviderAgrees is hash's property (#162; CONSTRAINTS C5;
// types.md, Maps): on each seed's keys, hash through a Hashing provider
// gives equal keys one hash and a Map keyed through the provider counts
// as the model does, at check time and in the test build lowered three
// ways: the IR (ergo test --native), the emitter (LowerTests, the
// fallback of a body the IR does not take, and every body of a census),
// and the emitter under the reference lowerings. Before the emitter lowered
// check.CallHash, its builds were an internal compiler error ("an unknown
// call kind"; examples/triage). A failing seed prints its source, the
// verdicts and the replay command.
//
// It builds Go because the oracle is the Go target's maphash: go/types
// says the call compiles, not that one seed is shared by every call. Each
// program runs under the kit's memory limit (runProgram).
//
// Mutant it kills (testdata/mutants.json): hash-emitter-fresh-seed.
func TestHashThroughProviderAgrees(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{}
	for s := uint64(1); s <= 12; s++ {
		seeds = append(seeds, s)
	}
	if s := os.Getenv("ERGO_HASH_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{v}
	}
	var builds []*lowered
	for _, seed := range seeds {
		src := hashProgram(seed)
		builds = append(builds, lowerTestBuild(t, fmt.Sprintf("hash_%d", seed), src))
		restore := referenceLowerings()
		ref, err := lowerTestBuildErr(fmt.Sprintf("hash_%d_ref", seed), src)
		restore()
		if err != nil {
			t.Fatal(err)
		}
		builds = append(builds, ref)
	}
	exes := buildAll(t, builds, true)
	if exes == nil {
		return
	}
	for _, seed := range seeds {
		src := hashProgram(seed)
		fail := func(format string, args ...any) {
			t.Helper()
			t.Errorf("seed %d: %s\n%s\nreplay: ERGO_HASH_SEED=%d go test -run '^TestHashThroughProviderAgrees$' ./internal/sketch/lower", seed, fmt.Sprintf(format, args...), src, seed)
		}
		for _, way := range []string{"", "_ref"} {
			stdout, stderr, _ := runExeArgs(t, exes[fmt.Sprintf("hash_%d%s", seed, way)])
			if got := nativeVerdict(stdout); got != "passed" {
				fail("the emitter's test build%s says %s:\n%s\n%s", way, got, stdout, firstLines(stderr, 6))
			}
		}
		// The IR: ergo test --native builds the test through the IR, as
		// examples/triage's is.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hash.ergo"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(ergoCLI(t), "test", dir, "--native")
		cmd.Dir = dir
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		_ = runProgram(t, cmd)
		if !slices.ContainsFunc(lines(out.String()), func(l string) bool {
			return (l == "ok V" || strings.HasPrefix(l, "ok V ")) && !strings.Contains(l, "(evaluated)")
		}) {
			fail("ergo test --native (the IR) does not pass V natively:\n%s", withoutBuildNote(out.Bytes()))
		}
		rep, err := evaluate(src)
		if err != nil {
			fail("%v", err)
			continue
		}
		if len(rep.Results) != 1 || string(rep.Results[0].Status) != "passed" {
			fail("the evaluator says %+v", rep.Results)
		}
	}
}
