package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/lint"
)

// changeSetOf is the record of the change from rev to the working tree of
// the program at path (anti-slop AS6): its added lines (addedLines), the
// files with their added and deleted line counts (an untracked file is all
// added), the packages of its Ergo files, the subjects of the commits
// from rev to HEAD, the files that changed together over the last
// lint.CoChangeCommits commits, and its purpose. Paths are keyed as
// addedLines keys them. A repository with no commit has no subjects and
// no co-change.
func changeSetOf(path, rev, purpose string) (*lint.ChangeSet, error) {
	added, err := addedLines(path, rev)
	if err != nil {
		return nil, err
	}
	root, single, err := diffRoot(path)
	if err != nil {
		return nil, err
	}
	git := gitAt(root)
	cs := &lint.ChangeSet{Base: rev, Added: added}
	_, headErr := git("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	hasHead := headErr == nil
	if hasHead || rev != "HEAD" {
		numstat, err := git("diff", "--numstat", "--no-color", "--no-ext-diff", "--relative", rev, "--", ".")
		if err != nil {
			return nil, err
		}
		cs.Files = lint.ParseNumstat(numstat)
	}
	untracked, err := git("ls-files", "--others", "--exclude-standard", "--", ".")
	if err != nil {
		return nil, err
	}
	for _, f := range strings.Split(untracked, "\n") {
		if f == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return nil, err
		}
		cs.Files = append(cs.Files, lint.FileChange{Path: f, Added: strings.Count(string(data), "\n")})
	}
	if single {
		base := filepath.Base(path)
		var mine []lint.FileChange
		for _, f := range cs.Files {
			if f.Path == base {
				f.Path = ""
				mine = append(mine, f)
			}
		}
		cs.Files = mine
	}
	cs.Packages = lint.PackagesOf(cs.Files)
	branch := ""
	if hasHead {
		if rev != "HEAD" {
			log, err := git("log", "--reverse", "--format=%x00%s", rev+"..HEAD", "--", ".")
			if err != nil {
				return nil, err
			}
			cs.Subjects, _ = lint.ParseLog(log)
		}
		log, err := git("log", fmt.Sprintf("-%d", lint.CoChangeCommits), "--name-only", "--relative", "--format=%x00%s", "HEAD", "--", ".")
		if err != nil {
			return nil, err
		}
		_, commits := lint.ParseLog(log)
		cs.CoChange = lint.CoChanges(commits)
		if b, err := git("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			branch = strings.TrimSpace(b)
		}
	}
	cs.Purpose = lint.PurposeOf(purpose, branch, cs.Subjects)
	return cs, nil
}
