package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// KeyedPermissions are the permission bits a file's key holds. Steps run as
// the user who owns the checkout, so the owner bits alone grant their access:
// owner-read decides whether a step can read a file. The three executable
// bits are data a check can read and decide what a step can execute. Group
// and other read and write bits are consulted by no process a step starts.
const KeyedPermissions fs.FileMode = 0o511

// RecentWindow is how close to a key's collection a stat-keyed path may
// change before the gate refuses to record under that key, as Go's test
// cache distrusts a file changed within 2 s (modTimeCutoff).
const RecentWindow = 2 * time.Second

// Classifier tells the class of a path that is not a directory.
type Classifier func(path string) Class

// Walk lists every path under root except root/.git, sorted by path, as the
// whole-gate key holds it (Entry). A regular file that Git tracks or sees
// untracked is keyed by content; everything else that is not a directory or
// a symlink is keyed by its stat tuple. Symlinks carry their target text;
// FollowLinks says where they lead. Recent names the stat-keyed paths changed
// within RecentWindow before now. A file whose content cannot be read makes
// the facts unreplayable.
func Walk(root string, classify Classifier, now time.Time) (entries []Entry, recent, unreplayable []string, err error) {
	type pending struct {
		index int
		name  string
		perm  fs.FileMode
	}
	var hashes []pending
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		e := Entry{Path: relative}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			e.Kind = Directory
		case mode&fs.ModeSymlink != 0:
			e.Kind, e.Class = Symlink, classify(relative)
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			e.Value = target
		case mode.IsRegular():
			e.Kind, e.Class = File, classify(relative)
			if e.Class != Ignored {
				hashes = append(hashes, pending{len(entries), name, mode.Perm() & KeyedPermissions})
				break
			}
			e.Value = statTuple(info)
			if changedSince(info, now.Add(-RecentWindow)) {
				recent = append(recent, relative)
			}
		default:
			e.Kind, e.Class = Other, classify(relative)
			e.Value = statTuple(info)
			if changedSince(info, now.Add(-RecentWindow)) {
				recent = append(recent, relative)
			}
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	problems := make([]string, len(hashes))
	var wg sync.WaitGroup
	work := make(chan int)
	for range max(2, runtime.NumCPU()) {
		wg.Go(func() {
			for i := range work {
				job := hashes[i]
				sum, err := fileSum(job.name)
				if err != nil {
					problems[i] = fmt.Sprintf("%s cannot be read: %v", entries[job.index].Path, err)
					continue
				}
				entries[job.index].Value = fmt.Sprintf("%v:%s", job.perm, sum)
			}
		})
	}
	for i := range hashes {
		work <- i
	}
	close(work)
	wg.Wait()
	for _, problem := range problems {
		if problem != "" {
			unreplayable = append(unreplayable, problem)
		}
	}
	return entries, recent, unreplayable, nil
}

// fileSum is the SHA-256 of a file's content, in hex.
func fileSum(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Links follows symlinks by the rule of the whole-gate key and of step
// receipts: a link keys its target text and what it reaches. A location
// inside the checkout (outside its .git) is covered by its own entry, and a
// location inside a keyed root (a fingerprinted installation such as
// GOROOT) by that fingerprint. A link that reaches anywhere else, directly,
// dangling or through a chain of links, or that loops, cannot be keyed.
type Links struct {
	root  string
	keyed []string
}

// NewLinks makes the link rule for the checkout at root and the keyed roots,
// each a directory or a file, by their resolved paths.
func NewLinks(root string, keyed []string) (Links, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Links{}, err
	}
	rule := Links{root: filepath.Clean(resolved)}
	for _, k := range keyed {
		if r, err := filepath.EvalSymlinks(k); err == nil {
			rule.keyed = append(rule.keyed, filepath.Clean(r))
		}
	}
	return rule, nil
}

// maxLinkHops bounds the links one lookup follows, as the kernel's 40.
const maxLinkHops = 40

// Follow reports where the link at relative (a path inside the checkout)
// leads: "checkout <path>" for a location in the checkout, "root <path>" for
// one in a keyed root, with "missing " before the path when the lookup
// stops at a name that does not exist. When the link cannot be keyed,
// problem names the target of the link in the chain that left the keyed
// roots, as an absolute path; reach is then empty.
func (l Links) Follow(relative string) (reach, problem string) {
	link := filepath.Join(l.root, filepath.FromSlash(relative))
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Sprintf("%s cannot be read: %v", relative, err)
	}
	// hop is the target of the last link followed, absolute.
	hop := absoluteTarget(filepath.Dir(link), target)
	outside := func() (string, string) {
		return "", fmt.Sprintf("%s reaches %s outside every keyed root", relative, hop)
	}
	current := filepath.Dir(link)
	if filepath.IsAbs(target) {
		current = string(filepath.Separator)
	}
	rest := components(target)
	hops := 1
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		switch name {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			if !l.passable(current) {
				return outside()
			}
			continue
		}
		next := filepath.Join(current, name)
		if !l.passable(next) {
			return outside()
		}
		info, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			// The lookup stops here: creating the name later changes the
			// entry or the fingerprint that covers it.
			if !l.covered(next) {
				return outside()
			}
			return "missing " + l.describe(next), ""
		}
		if err != nil {
			return "", fmt.Sprintf("%s: %v", relative, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			hops++
			if hops > maxLinkHops {
				return "", fmt.Sprintf("%s reaches a loop of links at %s", relative, next)
			}
			again, err := os.Readlink(next)
			if err != nil {
				return "", fmt.Sprintf("%s: %v", relative, err)
			}
			hop = absoluteTarget(current, again)
			if filepath.IsAbs(again) {
				current = string(filepath.Separator)
			}
			rest = append(components(again), rest...)
			continue
		}
		if len(rest) > 0 && !info.IsDir() {
			// A file used as a directory: the lookup stops at it.
			if !l.covered(next) {
				return outside()
			}
			return "missing " + l.describe(next) + "/" + path.Join(rest...), ""
		}
		current = next
	}
	if !l.covered(current) {
		return outside()
	}
	return l.describe(current), ""
}

// absoluteTarget is a link's target text as an absolute path, for a link in
// dir.
func absoluteTarget(dir, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Join(dir, target)
}

// Resolved is the location the link at relative leads to, when Follow keys
// it: an absolute path that is not a link, or "" when the lookup stops at a
// missing name.
func (l Links) Resolved(relative string) string {
	reach, problem := l.Follow(relative)
	if problem != "" || strings.HasPrefix(reach, "missing ") {
		return ""
	}
	if rest, ok := strings.CutPrefix(reach, "checkout "); ok {
		return filepath.Join(l.root, filepath.FromSlash(rest))
	}
	return strings.TrimPrefix(reach, "root ")
}

func (l Links) describe(place string) string {
	if within(place, l.root) {
		relative, _ := filepath.Rel(l.root, place)
		return "checkout " + filepath.ToSlash(relative)
	}
	return "root " + place
}

// covered reports whether a location is keyed: inside the checkout and not
// in its .git, or inside a keyed root.
func (l Links) covered(place string) bool {
	if within(place, l.root) && !within(place, filepath.Join(l.root, ".git")) {
		return true
	}
	for _, k := range l.keyed {
		if within(place, k) {
			return true
		}
	}
	return false
}

// passable reports whether a lookup may pass a location: a covered one, or
// a directory on the way to the checkout or a keyed root.
func (l Links) passable(place string) bool {
	if l.covered(place) || within(l.root, place) {
		return true
	}
	for _, k := range l.keyed {
		if within(k, place) {
			return true
		}
	}
	return false
}

// within reports whether name is dir or below it.
func within(name, dir string) bool {
	relative, err := filepath.Rel(dir, name)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func components(target string) []string {
	return strings.Split(filepath.ToSlash(target), "/")
}

// FollowLinks sets the Reach of every symlink entry and returns the
// problems of the links that cannot be keyed.
func (l Links) FollowLinks(entries []Entry) []string {
	var problems []string
	for i := range entries {
		if entries[i].Kind != Symlink {
			continue
		}
		reach, problem := l.Follow(entries[i].Path)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		entries[i].Reach = reach
	}
	return problems
}
