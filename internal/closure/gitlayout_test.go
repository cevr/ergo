package closure

import (
	"slices"
	"testing"
)

// gitLayouts are the inputs a test package records when it runs a
// read-only git command (git --no-optional-locks ls-files, rev-parse) at the
// module root, by checkout layout. Measured at batch-9 c863b89b: the
// worktree's entry from the full tier, the clone's from the merge tier's
// export (scripts/conformance, gate-merge.log); a clone on a branch read the
// same set (b9-fix-gate round-3 findings).
var gitLayouts = map[string][]entry{
	"worktree": {{"open", ".git"}, {"stat", ".git"}},
	"clone": {
		{"stat", ".git"}, {"open", ".git/HEAD"}, {"stat", ".git/HEAD"}, {"stat", ".git/commondir"},
		{"open", ".git/config"}, {"stat", ".git/config"}, {"open", ".git/index"},
		{"stat", ".git/objects"}, {"stat", ".git/refs"},
	},
}

// gitDeepReads are reads below the first level of .git, which no layout
// measured: a package that declares only git's metadata (not the whole
// tree) does not admit them, so a test that starts to read the objects or
// refs one by one shows in the sweep.
var gitDeepReads = []entry{{"open", ".git/objects/ab/cdef"}, {"open", ".git/refs/heads/main"}, {"list", ".git/refs/heads"}}

// TestGitMetadataVerdictIsTheLayoutsOwn is the guard of b9-fix-gate round 3:
// a package whose declared entries admit git's metadata in one checkout
// layout admits it in the other, so the sweep gives a worktree (the full
// tier) and a clone (the merge tier's export, where .git is a directory in
// the checkout) one verdict. At c863b89b scripts/conformance declared .git
// as a file: its sweep passed in a worktree and failed in the merge tier.
// Each declared entry that admits the metadata is used in both layouts (the
// sweep fails on an entry no recorded input uses), and a package that does
// not declare the whole tree admits nothing deeper than .git's members.
func TestGitMetadataVerdictIsTheLayoutsOwn(t *testing.T) {
	admitting := 0
	for dir, list := range declared {
		c := newClosure(dir, nil, nil)
		verdicts := map[string]int{}
		used := map[string]map[string]bool{}
		for layout, recorded := range gitLayouts {
			used[layout] = map[string]bool{}
			verdicts[layout] = len(c.check(recorded, used[layout]))
		}
		if verdicts["worktree"] == len(gitLayouts["worktree"]) && verdicts["clone"] == len(gitLayouts["clone"]) {
			continue // the package admits git's metadata in no layout
		}
		admitting++
		if verdicts["worktree"] != 0 || verdicts["clone"] != 0 {
			t.Errorf("%s: git's metadata lies outside its closure in one layout: %v of %d (worktree) and %d (clone) inputs; declared %v",
				dir, verdicts, len(gitLayouts["worktree"]), len(gitLayouts["clone"]), keys(list))
		}
		for key := range used["clone"] {
			if !used["worktree"][key] {
				t.Errorf("%s: the entry %s admits the clone's metadata and is unused in a worktree", dir, key)
			}
		}
		if slices.ContainsFunc(list, func(a allowance) bool { return a.path == "." && a.grant == subtree }) {
			continue // the whole tree, by design
		}
		if inside := len(gitDeepReads) - len(c.check(gitDeepReads, map[string]bool{})); inside != 0 {
			t.Errorf("%s: %d of the reads below .git's members %v lie inside its closure; declared %v", dir, inside, gitDeepReads, keys(list))
		}
	}
	if admitting == 0 {
		t.Fatal("no package admits git's metadata; scripts/conformance runs git ls-files")
	}
	// The check's controls: a .git file entry alone admits the worktree
	// only, and .git with .git/* admits both but leaves .git/* unused in a
	// worktree.
	c := newClosure("p", nil, nil)
	c.declared = []allowance{{".git", file, "control"}}
	if got := c.check(gitLayouts["clone"], map[string]bool{}); !slices.ContainsFunc(got, func(e entry) bool { return e.path == ".git/index" }) {
		t.Errorf("a .git file entry covers the clone's index: outside %v", got)
	}
	c.declared = []allowance{{".git", file, "control"}, {".git/*", files, "control"}}
	used := map[string]bool{}
	if got := c.check(gitLayouts["worktree"], used); len(got) != 0 || used["files .git/*"] {
		t.Errorf("a worktree: outside %v, used %v; want none outside and .git/* unused", got, used)
	}
}
