package task

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// missing is the digest of a path that does not exist (or is a folder
// where a file is read).
const missing = "-"

// Missing is missing for a Base: the listing of a folder that does not
// exist.
const Missing = missing

// fileStat is what a file's stat says of its content: when it is the same
// as when the file was hashed, the hash still holds.
type fileStat struct {
	size, mtime, ctime int64
	ino                uint64
	mode               fs.FileMode
}

// hasher hashes files of one tree, remembering each file's digest by its
// stat so that an unchanged file is not read again (a stat cache, as
// Git's index is).
type hasher struct {
	root  string
	known map[string]statDigest
	dirty bool
}

type statDigest struct {
	st     fileStat
	digest string
}

func newHasher(root string) *hasher { return &hasher{root: root, known: map[string]statDigest{}} }

// digest is the sha256 of the file rel, or missing.
func (h *hasher) digest(rel string) (string, error) {
	abs := filepath.Join(h.root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) || (err == nil && info.IsDir()) {
		return missing, nil
	}
	if err != nil {
		// A path through a file ("a/b" where a is a file) reads as missing.
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "not a directory") {
			return missing, nil
		}
		return "", err
	}
	st := statOf(info)
	if k, ok := h.known[rel]; ok && k.st == st && settled(st) {
		return k.digest, nil
	}
	var data []byte
	if info.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(abs)
		if err != nil {
			return "", err
		}
		data = []byte("link:" + target)
	} else if data, err = os.ReadFile(abs); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	d := hex.EncodeToString(sum[:])
	h.known[rel] = statDigest{st: st, digest: d}
	h.dirty = true
	return d, nil
}

// listing is the digest of the names folder rel holds, each with a mark
// for a folder; missing when there is no folder.
func listing(root, rel string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) || (err != nil && strings.Contains(err.Error(), "not a directory")) {
		return missing, nil
	}
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		mark := "f"
		if e.IsDir() {
			mark = "d"
		}
		io.WriteString(h, mark+e.Name()+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// statKey is the store key of the stat cache of the tree at root.
func statKey(root string) string { return lawcache.Key("task stat", root) }

// load reads the stat cache of h's tree from store.
func (h *hasher) load(store *lawcache.Store) {
	text, ok := store.Get(statKey(h.root))
	if !ok {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.SplitN(line, " ", 7)
		if len(f) != 7 {
			continue
		}
		var st fileStat
		var err error
		var mode uint64
		if st.size, err = strconv.ParseInt(f[0], 10, 64); err != nil {
			continue
		}
		if st.mtime, err = strconv.ParseInt(f[1], 10, 64); err != nil {
			continue
		}
		if st.ctime, err = strconv.ParseInt(f[2], 10, 64); err != nil {
			continue
		}
		if st.ino, err = strconv.ParseUint(f[3], 10, 64); err != nil {
			continue
		}
		if mode, err = strconv.ParseUint(f[4], 10, 32); err != nil {
			continue
		}
		st.mode = fs.FileMode(mode)
		h.known[f[6]] = statDigest{st: st, digest: f[5]}
	}
}

// save writes h's stat cache to store, leaving out each file changed in
// the last two seconds: its stat may not yet tell a later write in the
// same tick apart (Git's racy entries).
func (h *hasher) save(store *lawcache.Store) {
	if !h.dirty || store == nil {
		return
	}
	var b strings.Builder
	for _, rel := range sortedKeys(h.known) {
		k := h.known[rel]
		if !settled(k.st) {
			continue
		}
		b.WriteString(strconv.FormatInt(k.st.size, 10) + " " + strconv.FormatInt(k.st.mtime, 10) + " " +
			strconv.FormatInt(k.st.ctime, 10) + " " + strconv.FormatUint(k.st.ino, 10) + " " +
			strconv.FormatUint(uint64(k.st.mode), 10) + " " + k.digest + " " + rel + "\n")
	}
	store.Put(statKey(h.root), b.String())
}

// settled reports whether a stat is old enough to tell every later write
// apart: a file changed in the last two seconds may be written again in
// the same tick of the file system's clock with the same size, and then
// its stat would not show it (Git's racy entries), so its digest is never
// taken from the cache.
func settled(st fileStat) bool {
	racy := time.Now().Add(-2 * time.Second).UnixNano()
	return st.mtime < racy && st.ctime < racy
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return sortedUnique(keys)
}

// skipped reports whether a walk of a tree leaves out the slash path rel:
// the version control folder at the root and the copies Check makes.
func skipped(rel string) bool {
	first, _, _ := strings.Cut(rel, "/")
	return first == ".git" || first == scratchPrefix || strings.HasPrefix(first, scratchPrefix)
}

// snapshot is the stat of every file of the tree at root but the
// skipped ones, by slash path.
func snapshot(root string) (map[string]fileStat, error) {
	out := map[string]fileStat{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skipped(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[rel] = statOf(info)
		return nil
	})
	return out, err
}

// changed are the paths whose stat differs between two snapshots, or
// that one of them lacks, sorted.
func changed(before, after map[string]fileStat) []string {
	var out []string
	for p, st := range after {
		if b, ok := before[p]; !ok || b != st {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	return sortedUnique(out)
}

// under reports whether the slash path p is dir or below it.
func under(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// parentDir is the slash folder of p ("." at the root).
func parentDir(p string) string { return path.Dir(p) }
