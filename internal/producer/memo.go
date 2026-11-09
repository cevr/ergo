package producer

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The image memo spares a process the SHA-256 of its own image when an
// earlier process of the same file computed it. Hashing the image costs
// about 0.8 ms per MB on every command that prints facts, and the compiler
// is tens of MB.
//
// An entry maps a file key to the digest of the file's bytes. The key is
// what the kernel reports for the opened image (imageKey: device, inode,
// size, modification and change times), never an installation pathname.
// A file cannot be written while it executes (ETXTBSY), and any write to it
// sets its change time, which no caller can set back; a replaced file is
// another inode. So a key names one content for as long as an entry can
// see it, and a changed compiler gets a new key and is hashed again.
//
// The memo lives in the cache root whose modules the identity keys: whoever
// can write an entry can already write a published module. A malformed or
// unreadable memo is not an error; the image is hashed.
//
// Prior art: Git's index skips rehashing a file whose stat data (device,
// inode, size, mtime, ctime) is unchanged; ccache's direct mode trusts a
// recorded file's stat data the same way.
type imageMemo struct{ path string }

// memoFile names the memo inside its directory; memoEntries bounds it, so
// test binaries and old compilers do not grow it.
const (
	memoFile    = "producer-images"
	memoEntries = 16
	memoBytes   = 16 << 10
)

var memoDir struct {
	sync.Mutex
	dir string
}

// Remember lets the identity of this process recall and record the digest
// of its image in dir (the compiler's cache root). It must be called before
// the first Current; a later call does not change an acquired identity. A
// process that never calls it hashes its image every time (test binaries,
// embedding hosts).
func Remember(dir string) {
	memoDir.Lock()
	defer memoDir.Unlock()
	memoDir.dir = dir
}

// rememberedMemo is the memo Remember chose, or nil.
func rememberedMemo() *imageMemo {
	memoDir.Lock()
	defer memoDir.Unlock()
	if memoDir.dir == "" || !filepath.IsAbs(memoDir.dir) {
		return nil
	}
	return &imageMemo{path: filepath.Join(memoDir.dir, memoFile)}
}

// entries reads the memo, newest first; a malformed line is dropped.
func (m *imageMemo) entries() [][2]string {
	file, err := os.Open(m.path)
	if err != nil {
		return nil
	}
	defer file.Close()
	var out [][2]string
	scanner := bufio.NewScanner(io.LimitReader(file, memoBytes))
	for scanner.Scan() && len(out) < memoEntries {
		key, digest, ok := strings.Cut(scanner.Text(), " ")
		if ok && key != "" && !strings.ContainsAny(key, " \n") && validDigest(digest) {
			out = append(out, [2]string{key, digest})
		}
	}
	return out
}

func (m *imageMemo) recall(key string) (string, bool) {
	for _, entry := range m.entries() {
		if entry[0] == key {
			return entry[1], true
		}
	}
	return "", false
}

// record puts key first, written to a temporary file and renamed, so a
// reader sees the old memo or the new one. Two processes recording at once
// may lose one entry, which costs a hash, never a wrong digest.
func (m *imageMemo) record(key, digest string) {
	var buf bytes.Buffer
	buf.WriteString(key + " " + digest + "\n")
	kept := 1
	for _, entry := range m.entries() {
		if entry[0] != key && kept < memoEntries {
			buf.WriteString(entry[0] + " " + entry[1] + "\n")
			kept++
		}
	}
	dir := filepath.Dir(m.path)
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, memoFile+".*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(buf.Bytes())
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), m.path) != nil {
		os.Remove(tmp.Name())
	}
}

func validDigest(digest string) bool {
	hexDigits, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hexDigits) != 64 {
		return false
	}
	_, err := hex.DecodeString(hexDigits)
	return err == nil
}
