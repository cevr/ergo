package lawcache

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// randomParts is a seeded list of key parts drawn from a small alphabet,
// so equal parts, empty parts and parts that are prefixes of each other
// are common.
func randomParts(r *rand.Rand) []string {
	parts := make([]string, 1+r.IntN(5))
	for i := range parts {
		var b strings.Builder
		for range r.IntN(4) {
			b.WriteByte("ab:0\x00"[r.IntN(5)])
		}
		parts[i] = b.String()
	}
	return parts
}

// TestStaleEntryIsNeverServed is the store's soundness law (SHARED-RESULT-
// CACHE): an entry is served only for the very parts it was kept under.
// For seeded keys, each edit of the parts (one part changed, two adjacent
// parts joined, one part split in two, a part dropped or added) is a miss
// in a store that holds the entry, and the unedited parts are a hit, from
// a fresh process's view of the same disk. A failing seed prints the parts
// and the edit; replay with ERGO_LAWCACHE_SEED=N.
//
// It kills lawcache-key-unframed (parts written with no length, so a
// split or a join finds the entry) and lawcache-fresh-reads-disk (a
// --fresh store serves what another run kept).
func TestStaleEntryIsNeverServed(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if s := os.Getenv("ERGO_LAWCACHE_SEED"); s != "" {
		var seed uint64
		fmt.Sscan(s, &seed)
		seeds = []uint64{seed}
	}
	root := t.TempDir()
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 11))
		for trial := range 64 {
			// A store of its own for each trial: it holds the one entry.
			compiler := fmt.Sprintf("sha256:stale-law-%d-%d", seed, trial)
			parts := randomParts(r)
			kept := Open(root, compiler)
			kept.Put(Key(parts...), "ok kept\n")
			edits := map[string][]string{}
			i := r.IntN(len(parts))
			changed := append([]string(nil), parts...)
			changed[i] += "x"
			edits[fmt.Sprintf("part %d changed", i)] = changed
			edits["a part added"] = append(append([]string(nil), parts...), "")
			if len(parts) > 1 {
				j := r.IntN(len(parts) - 1)
				joined := append(append(append([]string(nil), parts[:j]...), parts[j]+parts[j+1]), parts[j+2:]...)
				edits[fmt.Sprintf("parts %d and %d joined", j, j+1)] = joined
				edits["the last part dropped"] = parts[:len(parts)-1]
			}
			if p := parts[i]; len(p) > 0 {
				k := r.IntN(len(p) + 1)
				split := append(append(append([]string(nil), parts[:i]...), p[:k], p[k:]), parts[i+1:]...)
				edits[fmt.Sprintf("part %d split at %d", i, k)] = split
			}
			// Another process: a store with nothing in memory.
			other := Memory(compiler)
			other.dir = kept.dir
			if text, ok := other.Get(Key(parts...)); !ok || text != "ok kept\n" {
				t.Fatalf("seed %d trial %d: the kept entry of %q is not found (%q %v); replay with ERGO_LAWCACHE_SEED=%d", seed, trial, parts, text, ok, seed)
			}
			for edit, edited := range edits {
				if _, ok := other.Get(Key(edited...)); ok {
					t.Fatalf("seed %d trial %d: %q kept, %s (%q) is served it; replay with ERGO_LAWCACHE_SEED=%d", seed, trial, parts, edit, edited, seed)
				}
			}
			fresh := Memory(compiler)
			fresh.dir = kept.dir
			fresh.Fresh()
			if _, ok := fresh.Get(Key(parts...)); ok {
				t.Fatalf("seed %d trial %d: a fresh store served another run's entry", seed, trial)
			}
			fresh.Put(Key(parts...), "ok again\n")
			if text, ok := fresh.Get(Key(parts...)); !ok || text != "ok again\n" {
				t.Fatalf("seed %d trial %d: a fresh store lost its own pass (%q %v)", seed, trial, text, ok)
			}
		}
	}
}

// TestWithinNamesNoWorkspace is Within's law: for seeded paths below or
// beside a root, Unroot(root, Within(root, p)) is p; a path below the
// root is written the same from every root that holds the same tree, and
// a path that is not below it (a sibling whose name the root's is a
// prefix of, the root's parent) stays absolute. Names finds the root in a
// text as written or through a link. It kills lawcache-within-escapes-root
// (a sibling folder /w/onex written as a path below /w/one) and lawcache-names-unresolved
// (an answer naming the root's resolved path is shared).
func TestWithinNamesNoWorkspace(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	for range 256 {
		var segs []string
		for range 1 + r.IntN(3) {
			segs = append(segs, []string{"a", "b", "a.b", "..x", "c"}[r.IntN(5)])
		}
		rel := filepath.Join(segs...)
		roots := []string{"/w/one", "/srv/two/three"}
		for _, root := range roots {
			below := filepath.Join(root, rel)
			beside := root + "x/" + rel
			for _, p := range []string{below, beside, filepath.Dir(root), root} {
				if got := Unroot(root, Within(root, p)); got != p {
					t.Fatalf("Unroot(%q, Within(%q, %q)) = %q", root, root, p, got)
				}
			}
			if w := Within(root, below); w != Within(roots[0], filepath.Join(roots[0], rel)) || !strings.HasPrefix(w, "@") {
				t.Fatalf("Within(%q, %q) = %q: not the same from every root", root, below, w)
			}
			if w := Within(root, beside); w != beside {
				t.Fatalf("Within(%q, %q) = %q: a sibling of the root is below it", root, beside, w)
			}
			if w := Within(root, filepath.Dir(root)); w != filepath.Dir(root) {
				t.Fatalf("Within(%q, its parent) = %q", root, w)
			}
		}
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if !Names("at "+link+"/x.ergo", link) || !Names("at "+resolved+"/x.ergo", link) || Names("at @/x.ergo", link) || Names("anything", "") {
		t.Fatalf("Names does not find the root %q (resolved %q) exactly", link, resolved)
	}
}
