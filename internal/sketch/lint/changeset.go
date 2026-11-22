package lint

import (
	"cmp"
	"path"
	"slices"
	"strconv"
	"strings"
)

// ChangeSet is what `ergo lint --diff` knows of the change it lints
// (anti-slop AS6): one record, built from git once per run, that the
// change rules (added-clone, scattered-edit) and the report read. Paths are
// relative to the module root, as Finding.File is.
type ChangeSet struct {
	// Base is the revision the change is compared with (--diff=REV).
	Base string `json:"base"`
	// Files are the changed files, by path, with their added and deleted
	// line counts; an untracked file is all added.
	Files []FileChange `json:"files"`
	// Packages are the directories of the changed Ergo source files.
	Packages []string `json:"packages"`
	// Subjects are the subjects of the commits from Base to HEAD, oldest
	// first; none when Base is HEAD.
	Subjects []string `json:"subjects"`
	// CoChange are the pairs of files that changed in one commit, over the
	// last CoChangeCommits commits, counted (CoChanges).
	CoChange []CoChange `json:"coChange"`
	// Purpose is the change's stated purpose: --purpose, else the branch
	// and the subjects.
	Purpose string `json:"purpose"`
	// Added are the added lines, by file (InDiff's scope).
	Added Added `json:"-"`
}

// FileChange is one changed file.
type FileChange struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

// CoChange is two files that changed together Count times; A sorts before B.
type CoChange struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Count int    `json:"count"`
}

// CoChangeCommits is how many recent commits the co-change pairs read, and
// CoChangeMaxFiles the size of a commit past which it is skipped: a
// formatting or rename sweep says nothing of what changes together.
const (
	CoChangeCommits  = 200
	CoChangeMaxFiles = 50
)

// CoChanges counts each pair of distinct files that one commit changed,
// over commits, each a list of paths; a commit of more than
// CoChangeMaxFiles files is skipped, and a path listed twice in one commit
// counts once. The pairs are sorted by count, most first, then by path.
func CoChanges(commits [][]string) []CoChange {
	counts := map[[2]string]int{}
	for _, paths := range commits {
		files := slices.Clone(paths)
		slices.Sort(files)
		files = slices.Compact(files)
		if len(files) > CoChangeMaxFiles {
			continue
		}
		for i := range files {
			a := files[i]
			for _, b := range files[i+1:] {
				counts[[2]string{a, b}]++
			}
		}
	}
	out := make([]CoChange, 0, len(counts))
	for k, n := range counts {
		out = append(out, CoChange{A: k[0], B: k[1], Count: n})
	}
	slices.SortFunc(out, func(x, y CoChange) int {
		return cmp.Or(cmp.Compare(y.Count, x.Count), cmp.Compare(x.A, y.A), cmp.Compare(x.B, y.B))
	})
	return out
}

// ParseNumstat reads `git diff --numstat` (added, deleted, path per line;
// a binary file's "-" counts are 0).
func ParseNumstat(text string) []FileChange {
	var out []FileChange
	for _, line := range strings.Split(text, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			continue
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		out = append(out, FileChange{Path: fields[2], Added: added, Deleted: deleted})
	}
	return out
}

// ParseLog reads `git log --name-only --format=%x00%s`: each commit's
// subject and the paths it changed, newest first as git prints them.
func ParseLog(text string) (subjects []string, commits [][]string) {
	for _, block := range strings.Split(text, "\x00")[1:] {
		lines := strings.Split(block, "\n")
		subjects = append(subjects, lines[0])
		var paths []string
		for _, p := range lines[1:] {
			if p != "" {
				paths = append(paths, p)
			}
		}
		commits = append(commits, paths)
	}
	return subjects, commits
}

// PackagesOf is the sorted directories of the Ergo source files among
// files ("." for the module root).
func PackagesOf(files []FileChange) []string {
	var out []string
	for _, f := range files {
		// A changed path is a slash path relative to the module root.
		if path.Ext(f.Path) != ".ergo" {
			continue
		}
		out = append(out, path.Dir(f.Path))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// PurposeOf is the stated purpose, else the branch and the subjects.
func PurposeOf(stated, branch string, subjects []string) string {
	if stated != "" {
		return stated
	}
	parts := []string{"branch " + branch}
	return strings.Join(append(parts, subjects...), "; ")
}
