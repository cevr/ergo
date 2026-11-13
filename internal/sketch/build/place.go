package build

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"time"

	"ergo.local/prototype/internal/stage"
)

// The output of a direct build is a hard link of the executable's blob in
// the store (compile-speed plan CS2, models/warmstamp): the warm answer
// then checks "the store has the blob, and the output is that same inode"
// with two stat calls and no timestamps, and placing the output writes no
// byte. The blob is read-only (mode 0555), so the output is too; go build
// gives its output 0777 less the umask. An output on another file system
// than the store cannot be a link: it is a copy, written again on every
// build, and the warm answer copies it again too (docs/spec/build.md, Warm
// answer).

// ExecutableHeld reports whether output is the store's executable blob
// digest: the blob is present and output names the same file. It marks
// the blob used, as a build that reuses it does.
func (c Cache) ExecutableHeld(digest, output string) bool {
	blob := c.executableBlob(digest)
	info, err := os.Stat(blob)
	if err != nil {
		return false
	}
	out, err := os.Stat(output)
	if err != nil || !os.SameFile(info, out) {
		return false
	}
	mark(blob, info, time.Now())
	return true
}

// HasExecutable reports whether the store holds the blob digest.
func (c Cache) HasExecutable(digest string) bool {
	_, err := os.Stat(c.executableBlob(digest))
	return err == nil
}

// PlaceExecutable puts the store's executable blob digest at output by a
// rename, so the output is never a partial file: a hard link of the blob,
// or a copy when the link is refused (another file system). An output that
// already is the blob is left as it is.
func (c Cache) PlaceExecutable(digest, output string) error {
	absolute, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	blob := c.executableBlob(digest)
	if c.ExecutableHeld(digest, absolute) {
		return nil
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := filepath.Join(filepath.Dir(absolute), "."+filepath.Base(absolute)+".tmp-"+hex.EncodeToString(nonce[:]))
	defer os.Remove(name)
	if err := os.Link(blob, name); err != nil {
		if err := copyExecutable(blob, name); err != nil {
			return err
		}
	}
	if err := os.Rename(name, absolute); err != nil {
		return err
	}
	stage.Add(stage.OutputsPlaced, 1)
	return nil
}

// copyExecutable copies the blob to name, mode 0777 less the umask, the
// mode go build gives an executable.
func copyExecutable(blob, name string) error {
	src, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o777)
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, src)
	stage.Add(stage.BytesWritten, n)
	if err != nil {
		tmp.Close()
		return err
	}
	return tmp.Close()
}

func (c Cache) executableBlob(digest string) string {
	return filepath.Join(c.Root, "go", "blobs", digest)
}
