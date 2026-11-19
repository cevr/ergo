package residue_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// patchHunk is one hunk of the IR compiler patch in a file Go already
// has: the file, its added and deleted lines, and its key, the hunk's first
// changed line with its runs of spaces made one.
type patchHunk struct {
	file     string
	add, del int
	key      string
}

var spaceRun = regexp.MustCompile(`\s+`)

// existingHunks reads a git patch and returns its hunks in files that
// exist before it (a new file's block, "--- /dev/null", is ours) and the
// number of new files.
func existingHunks(patch string) ([]patchHunk, int) {
	var hunks []patchHunk
	fresh := 0
	for _, block := range strings.Split("\n"+patch, "\ndiff --git ")[1:] {
		lines := strings.Split(block, "\n")
		file := strings.TrimPrefix(strings.Fields(lines[0])[0], "a/")
		isNew := false
		var cur *patchHunk
		for _, l := range lines[1:] {
			switch {
			case l == "--- /dev/null":
				isNew = true
			case strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ "):
			case strings.HasPrefix(l, "@@"):
				hunks = append(hunks, patchHunk{file: file})
				cur = &hunks[len(hunks)-1]
			case cur != nil && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")):
				if l[0] == '+' {
					cur.add++
				} else {
					cur.del++
				}
				if cur.key == "" {
					cur.key = strings.TrimSpace(spaceRun.ReplaceAllString(l[1:], " "))
				}
			}
		}
		if isNew {
			fresh++
			hunks = slices.DeleteFunc(hunks, func(h patchHunk) bool { return h.file == file })
		}
	}
	return hunks, fresh
}

// hookLine is a line of toolchain/ir/hooks: file | +A -D | key | reason.
type hookLine struct {
	n              int
	file, key, why string
	add, del       int
	matched        bool
}

// TestPatchHooksBudgeted holds the IR compiler patch to thin hooks in Go's
// own files (common.md MINIMAL FORK PATCH, owner 2026-11-16: "as minimal
// as a large patch can be"; PATCH-MIN). Every hunk the patch makes in a
// file Go already has is a line of toolchain/ir/hooks, with its exact
// count of added and deleted lines and the reason gc must call our code
// there; the file's total is the sum. A hunk with no line, a count that
// differs from its line, a line with no reason, a line no hunk matches,
// and a total that is not the sum all fail. So a unit that adds or grows a
// hook edits the hooks file and says why; a hook that shrinks or goes
// updates the tracked number. Our code in new files (the -ergoir reader)
// is not counted: a new file never conflicts with a Go release.
//
// Mutants: patch-hook-unlisted (a hunk's line removed from the hooks
// file), patch-hook-grown (a hunk grows past its line's count).
func TestPatchHooksBudgeted(t *testing.T) {
	root := filepath.Join("..", "..")
	lock, err := os.ReadFile(filepath.Join(root, "toolchain", "ir", "lock"))
	if err != nil {
		t.Fatal(err)
	}
	var patches []string
	for _, l := range strings.Split(string(lock), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "patch" {
			patches = append(patches, f[1])
		}
	}
	if len(patches) != 1 {
		t.Fatalf("toolchain/ir/lock names %d patches (%v); the hooks file budgets one", len(patches), patches)
	}
	data, err := os.ReadFile(filepath.Join(root, "toolchain", "ir", patches[0]))
	if err != nil {
		t.Fatal(err)
	}
	hunks, fresh := existingHunks(string(data))
	if fresh == 0 {
		t.Fatalf("%s: no new file; the reader is ours and lives in new files", patches[0])
	}
	budget, err := os.ReadFile(filepath.Join(root, "toolchain", "ir", "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []*hookLine
	totalAdd, totalDel, haveTotal := 0, 0, false
	for i, l := range strings.Split(string(budget), "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(l, "total "); ok {
			if _, err := fmt.Sscanf(rest, "+%d -%d", &totalAdd, &totalDel); err != nil {
				t.Errorf("hooks:%d: total %q is not +A -D", i+1, rest)
			}
			haveTotal = true
			continue
		}
		parts := strings.SplitN(l, " | ", 4)
		if len(parts) != 4 {
			t.Errorf("hooks:%d: %q is not file | +A -D | first changed line | reason", i+1, l)
			continue
		}
		h := &hookLine{n: i + 1, file: parts[0], key: strings.TrimSpace(spaceRun.ReplaceAllString(parts[2], " ")), why: strings.TrimSpace(parts[3])}
		if _, err := fmt.Sscanf(parts[1], "+%d -%d", &h.add, &h.del); err != nil {
			t.Errorf("hooks:%d: count %q is not +A -D", h.n, parts[1])
		}
		if len(h.key) < 12 {
			t.Errorf("hooks:%d: key %q is too short to name one hunk", h.n, h.key)
		}
		if len(h.why) < 12 {
			t.Errorf("hooks:%d: %s's hunk has no reason", h.n, h.file)
		}
		lines = append(lines, h)
	}
	sumAdd, sumDel := 0, 0
	for _, hk := range hunks {
		sumAdd += hk.add
		sumDel += hk.del
		var found *hookLine
		for _, h := range lines {
			if h.file == hk.file && !h.matched && strings.HasPrefix(hk.key, h.key) {
				found = h
				break
			}
		}
		if found == nil {
			t.Errorf("%s: the hunk %q (+%d -%d) in Go's own file has no line in toolchain/ir/hooks; add one with the reason gc must call our code there, or move the code into a new file", hk.file, hk.key, hk.add, hk.del)
			continue
		}
		found.matched = true
		if found.add != hk.add || found.del != hk.del {
			t.Errorf("hooks:%d: %s's hunk %q is +%d -%d in the patch, +%d -%d in the hooks file; a hook that grows says why on its line", found.n, hk.file, hk.key, hk.add, hk.del, found.add, found.del)
		}
	}
	for _, h := range lines {
		if !h.matched {
			t.Errorf("hooks:%d: no hunk of %s starts with %q; drop the line", h.n, h.file, h.key)
		}
	}
	if !haveTotal || totalAdd != sumAdd || totalDel != sumDel {
		t.Errorf("toolchain/ir/hooks total is +%d -%d (present: %v); the patch's hunks in Go's own files sum to +%d -%d", totalAdd, totalDel, haveTotal, sumAdd, sumDel)
	}
}
