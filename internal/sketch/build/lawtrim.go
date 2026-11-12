package build

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// The law verdict store (internal/sketch/lawcache) keeps one small file
// per verdict under Root/laws/v1/<2 hex>/<name>, written by a rename and
// marked used (its modification time) at most once per markInterval when
// a command reads it. The emission cache's trim bounds it as it bounds
// Root/modules: a due trim removes the verdicts unused for unusedLimit and
// the temporary files a crashed writer left (older than stagingLifetime),
// and past lawCap verdicts the pass removes the least recently used, the
// whole excess in one pass. A verdict file is read whole and never held,
// so its removal needs no lock: a command that misses it runs the law
// again and writes it anew.

// lawsDir is the store's directory in Root (lawcache.Open).
var lawsDir = filepath.Join("laws", "v1")

// lawCap is the most verdicts Root/laws keeps (type variable: tests set
// it small). A verdict is one short line, under a kilobyte with its
// inode, so the cap bounds the store near 64 MB.
var lawCap = 65536

// LawsWritten is the trim after a command that wrote n law verdicts to
// the store under c.Root: when n is not 0, it counts the verdicts and,
// past lawCap, runs the cap pass, as a build that published runs the
// modules' (trimAfter). A due trim runs too.
func (c Cache) LawsWritten(n int) {
	if n > 0 {
		c.trimPass(time.Now(), false, true)
	}
}

// lawsOverCap reports whether Root/laws holds more than lawCap verdicts.
// It stats none.
func (c Cache) lawsOverCap() bool {
	seen := 0
	shards, err := os.ReadDir(filepath.Join(c.Root, lawsDir))
	if err != nil {
		return false
	}
	for _, s := range shards {
		names, err := os.ReadDir(filepath.Join(c.Root, lawsDir, s.Name()))
		if err != nil {
			continue
		}
		seen += len(names)
		if seen > lawCap {
			return true
		}
	}
	return false
}

// trimLaws bounds Root/laws: when due, it removes each verdict unused
// for unusedLimit and each temporary file older than stagingLifetime;
// then, past lawCap, the least recently used verdicts, the whole excess.
// It returns how many files it removed.
func (c Cache) trimLaws(now time.Time, due bool) int {
	type used struct {
		path string
		mark time.Time
	}
	var all []used
	removed := 0
	root := filepath.Join(c.Root, lawsDir)
	shards, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, s := range shards {
		dir := filepath.Join(root, s.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			temporary := strings.HasSuffix(e.Name(), ".part")
			switch {
			case temporary && now.Sub(info.ModTime()) > stagingLifetime,
				!temporary && due && now.Sub(info.ModTime()) > unusedLimit:
				if os.Remove(path) == nil {
					removed++
				}
			case !temporary:
				all = append(all, used{path, info.ModTime()})
			}
		}
	}
	if len(all) <= lawCap {
		return removed
	}
	slices.SortStableFunc(all, func(a, b used) int { return a.mark.Compare(b.mark) })
	for _, e := range all[:len(all)-lawCap] {
		if os.Remove(e.path) == nil {
			removed++
		}
	}
	return removed
}
