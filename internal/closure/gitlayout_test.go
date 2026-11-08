package closure

import (
	"slices"
	"testing"
)

// gitLayouts are the inputs a test package records when it runs a
// read-only git command (git --no-optional-locks ls-files, rev-parse) at the
// module root, by checkout layout. Measured at batch-9 c863b89b: the
// worktree's entry from the full tier, the clone's from the merge tier's
// export (scripts/conformance, gate-merge.log). The last row's refs
// (packed-refs, refs/heads/main) are inferred, not measured: what a clone
// whose HEAD names a branch would read (the round-3 verifier's branch clone
// did not read them).
var gitLayouts = map[string][]entry{
	"worktree": {{"open", ".git"}, {"stat", ".git"}},
	"clone": {
		{"stat", ".git"}, {"open", ".git/HEAD"}, {"stat", ".git/HEAD"}, {"stat", ".git/commondir"},
		{"open", ".git/config"}, {"stat", ".git/config"}, {"open", ".git/index"},
		{"stat", ".git/objects"}, {"stat", ".git/refs"},
		{"open", ".git/packed-refs"}, {"open", ".git/refs/heads/main"}, {"stat", ".git/refs/heads/main"},
	},
}

// TestGitMetadataVerdictIsTheLayoutsOwn is the guard of b9-fix-gate round 3:
// a package whose declared entries admit git's metadata in one checkout
// layout admits it in the other, so the sweep gives a worktree (the full
// tier) and a clone (the merge tier's export, where .git is a directory in
// the checkout) one verdict. At c863b89b scripts/conformance declared .git
// as a file: its sweep passed in a worktree and failed in the merge tier.
func TestGitMetadataVerdictIsTheLayoutsOwn(t *testing.T) {
	admitting := 0
	for dir, entries := range declared {
		c := newClosure(dir, nil, nil)
		verdicts := map[string]int{}
		for layout, recorded := range gitLayouts {
			verdicts[layout] = len(c.check(recorded, map[string]bool{}))
		}
		if verdicts["worktree"] == len(gitLayouts["worktree"]) && verdicts["clone"] == len(gitLayouts["clone"]) {
			continue // the package admits git's metadata in no layout
		}
		admitting++
		if verdicts["worktree"] != 0 || verdicts["clone"] != 0 {
			t.Errorf("%s: git's metadata lies outside its closure in one layout: %v of %d (worktree) and %d (clone) inputs; declared %v",
				dir, verdicts, len(gitLayouts["worktree"]), len(gitLayouts["clone"]), entries)
		}
	}
	if admitting == 0 {
		t.Fatal("no package admits git's metadata; scripts/conformance runs git ls-files")
	}
	// The check's control: a .git file entry alone admits the worktree only.
	c := newClosure("p", nil, nil)
	c.declared = []allowance{{".git", file, "control"}}
	if got := c.check(gitLayouts["clone"], map[string]bool{}); !slices.ContainsFunc(got, func(e entry) bool { return e.path == ".git/index" }) {
		t.Errorf("a .git file entry covers the clone's index: outside %v", got)
	}
}
