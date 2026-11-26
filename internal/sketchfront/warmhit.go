package sketchfront

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/sketch/build"
)

// listingsFresh reports whether every folder the request listed still has
// its listing (listing gives a folder's digest), and every path it looked
// up still has its kind.
func (in Inputs) listingsFresh(listing func(dir string) (string, bool)) bool {
	fresh := true
	for dir, digest := range in.Dirs {
		if got, ok := listing(dir); !ok || got != digest {
			fresh = false
		}
	}
	for name, kind := range in.Stats {
		stat, prefix := os.Stat, ""
		if strings.HasPrefix(kind, "l") {
			stat, prefix = os.Lstat, "l"
		}
		info, err := stat(name)
		got := prefix + "missing"
		if err == nil {
			got = prefix + kindOf(info)
		} else if !errors.Is(err, fs.ErrNotExist) {
			got = "error"
		}
		if got != kind {
			fresh = false
		}
	}
	return fresh
}

// hostListing is a folder's listing digest, read now.
func hostListing(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	return listingDigest(entries), true
}

// WarmHit is WarmLookup for a process that answers many requests (ergo
// dev): it answers the request key from its manifest when every input
// still has the content the manifest holds. sums keeps, between requests,
// each input file's hash, each folder's listing digest and the decoded
// manifest, each for as long as its file's stat says it has not changed. It
// reads nothing for a miss's normal path: ok false is "not served". root
// is the request's module folder, which the manifest's relative paths are
// read under (Recorder.Record).
func WarmHit(c build.Cache, key, root string, valid func(WarmAnswer) bool, sums *StatSums) (answer WarmAnswer, ok bool) {
	if key == "" {
		return WarmAnswer{}, false
	}
	m, ok := sums.manifest(c, key, root)
	if !ok {
		return WarmAnswer{}, false
	}
	for name, want := range m.Inputs.Files {
		sum, size, ok := sums.Sum(name)
		if !ok || size != want.Size || sum != want.SHA256 {
			return WarmAnswer{}, false
		}
	}
	if !m.Inputs.listingsFresh(sums.Listing) || !valid(m.Answer) {
		return WarmAnswer{}, false
	}
	return m.Answer, true
}

// FileStat is what a file's stat says of its content: its size, its
// modification and change times, and its device and inode. A write to the
// file, or a new file under its name, changes its change time, which no
// user call sets back.
type FileStat struct {
	Size, Mtime, Ctime int64
	Dev, Ino           uint64
}

// settle is how long before it was read a file's last change must be for
// its stat to stand for its content: a write within the file system's
// timestamp tick of an earlier one may leave the stat as it was (Git's
// "racily clean" entry).
const settle = time.Second

// StatSums keeps what one process read of a file between its requests,
// with the stat the file had then: a file whose stat is the same, and whose
// last change was settle before that read, is not read again; any other
// file is read again.
type StatSums struct {
	// Stat and Now are the host's (statFile, time.Now) unless a test
	// sets them.
	Stat func(name string) (FileStat, bool)
	Now  func() time.Time

	mu   sync.Mutex
	kept map[string]keptRead
}

type keptRead struct {
	stat  FileStat
	value any
}

// keep is read's value for the file name, kept under tag+name.
func (s *StatSums) keep(tag, name string, read func() (any, bool)) (any, bool) {
	stat, now := s.Stat, s.Now
	if stat == nil {
		stat = statFile
	}
	if now == nil {
		now = time.Now
	}
	before, stated := stat(name)
	if stated {
		s.mu.Lock()
		k, found := s.kept[tag+name]
		s.mu.Unlock()
		if found && k.stat == before {
			return k.value, true
		}
	}
	at := now()
	value, ok := read()
	if !ok {
		return nil, false
	}
	after, again := stat(name)
	settled := at.Sub(time.Unix(0, max(after.Mtime, after.Ctime))) > settle
	if stated && again && before == after && settled {
		s.mu.Lock()
		if s.kept == nil {
			s.kept = map[string]keptRead{}
		}
		s.kept[tag+name] = keptRead{stat: after, value: value}
		s.mu.Unlock()
	}
	return value, true
}

type fileSum struct {
	sum  string
	size int64
}

// Sum is the SHA-256 and size of name's content.
func (s *StatSums) Sum(name string) (sum string, size int64, ok bool) {
	v, ok := s.keep("f:", name, func() (any, bool) {
		data, err := hostFS{}.ReadFile(name)
		if err != nil {
			return nil, false
		}
		return fileSum{sum: hashInput(data), size: int64(len(data))}, true
	})
	if !ok {
		return "", 0, false
	}
	f := v.(fileSum)
	return f.sum, f.size, true
}

// Listing is the digest of dir's listing. A folder's change time changes
// when an entry is added, removed or renamed.
func (s *StatSums) Listing(dir string) (string, bool) {
	v, ok := s.keep("d:", dir, func() (any, bool) {
		digest, ok := hostListing(dir)
		return digest, ok
	})
	if !ok {
		return "", false
	}
	return v.(string), true
}

// manifest is the request key's manifest, its paths read under root. A
// kept one is marked used, as WarmManifest marks the one it reads; it is
// kept for each root apart, since one key serves every workspace.
func (s *StatSums) manifest(c build.Cache, key, root string) (warmManifest, bool) {
	read := false
	v, ok := s.keep("m:"+root+"\x00", c.WarmManifestFile(key), func() (any, bool) {
		read = true
		data, found := c.WarmManifest(key)
		var m warmManifest
		if !found || json.Unmarshal(data, &m) != nil || m.Schema != warmSchema || m.Key != key {
			return nil, false
		}
		m.Inputs = m.Inputs.Under(root)
		return m, true
	})
	if !ok {
		return warmManifest{}, false
	}
	if !read {
		c.MarkWarmManifest(key)
	}
	return v.(warmManifest), true
}
