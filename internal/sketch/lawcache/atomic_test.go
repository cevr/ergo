package lawcache

import (
	"strings"
	"testing"
)

// TestEntriesPublishWhole: a verdict entry is published whole or not at
// all (prior-bun2 item 3). Midway through an entry's write (midWrite):
//
//   - a reader in another process (a fresh store of the same root and
//     compiler) finds no entry, never half of one;
//   - a second writer of the same key writes its whole entry; when the
//     first writer finishes, the entry is one writer's text, never a mix.
//
// Mutants only it kills (testdata/mutants.json): lawcache-write-in-place
// (the entry is written at its place, so a reader meets half of it) and
// lawcache-shared-part-name (both writers build in one file, so the
// entry mixes their texts). The store has no other test of its disk.
func TestEntriesPublishWhole(t *testing.T) {
	root := t.TempDir()
	key := Key("law", "Torn")
	first := strings.Repeat("ok law Torn (100 cases) first\n", 64)
	second := strings.Repeat("ok law Torn (100 cases) other\n", 64)

	// A reader midway.
	reads := 0
	midWrite = func(string) {
		midWrite = nil
		reads++
		if text, ok := Memory("c1").withDir(root).Get(key); ok {
			t.Fatalf("a reader midway through the write found an entry of %d bytes, want none", len(text))
		}
	}
	Memory("c1").withDir(root).Put(key, first)
	if reads != 1 {
		t.Fatal("the write never reached midWrite")
	}
	if text, ok := Memory("c1").withDir(root).Get(key); !ok || text != first {
		t.Fatalf("after the write the entry is %d bytes (found %v), want the whole %d", len(text), ok, len(first))
	}

	// A second writer midway, on a fresh root.
	root = t.TempDir()
	midWrite = func(string) {
		midWrite = nil
		Memory("c2").withDir(root).Put(key, second)
	}
	Memory("c2").withDir(root).Put(key, first)
	text, ok := Memory("c2").withDir(root).Get(key)
	if !ok || (text != first && text != second) {
		t.Fatalf("two writers of one key left an entry of %d bytes (found %v) that is neither writer's whole text", len(text), ok)
	}
}

// withDir is s with its disk under root, as Open gives a store of a
// compiler of content identity.
func (s *Store) withDir(root string) *Store {
	s.dir = root + "/laws/v1"
	return s
}
