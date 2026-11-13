package build

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The executable entries (fu-test-bins): a module linked for a command
// that only runs it (ergo test) is linked into Root/bins/KEY-VARIANT, the
// entry of the module's key and of the toolchain's target, flags and tags,
// instead of a fresh temporary directory. The go command links only when
// the target's build ID does not match the link action's (cmd/go's own
// up-to-date check), so a warm run of an unchanged package runs no link:
// `ergo test ./...` over N unchanged packages links none of them.
//
// An entry is held like a module entry: the run holds a shared flock on the
// entry directory from before the link until it releases the module, after
// the executable exits, so a trim, which moves a directory only under an
// exclusive flock, never removes an executable that runs. Links into one
// entry take turns on the entry's link.lock, so no process runs a file
// another process is still writing; a link that finds the target up to
// date writes nothing. The trim walks Root/bins as it walks Root/modules,
// and removes an entry that no run used for unusedLimit.
//
// A private module (a process-scoped compiler) has no executable entry, and
// a platform without flock has none: no lock could keep a running file from
// a trim or a concurrent link. Those runs link to the caller's own output.
//
// Prior art: go run and go tool cache the executables they link (Go 1.24,
// in the build cache, keyed by the link action); go test caches the test
// binary's result, keyed by the binary and its inputs.

// ErrNoExecutableCache reports a module that has no executable entry: the
// caller links to its own output.
var ErrNoExecutableCache = errors.New("build: no executable entry for this module")

// executableVariant names the toolchain's link of a module: two toolchains
// that differ in the go command, the target, the flags or the tags have two
// entries, so they never relink each other's executable.
func executableVariant(t Toolchain) string {
	h := sha256.New()
	for _, s := range []string{t.Go, t.GOOS, t.GOARCH, t.GOFLAGS, strings.Join(t.Tags, ",")} {
		fmt.Fprintf(h, "%d:%s\x00", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// LinkCached links module m with toolchain t to the executable name in the
// module's executable entry, and returns m with Executable set, held until
// its Release: the caller runs the executable and then releases it. The
// release of m itself stays the caller's. It returns ErrNoExecutableCache
// for a private module or where flock does not exist.
func (c Cache) LinkCached(m Module, t Toolchain, name string, stderr io.Writer) (Module, error) {
	if m.Private || !leasesSupported {
		return Module{}, ErrNoExecutableCache
	}
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return Module{}, fmt.Errorf("build: executable name %q is not a file name", name)
	}
	bins := filepath.Join(c.Root, "bins")
	if err := os.MkdirAll(bins, 0o755); err != nil {
		return Module{}, err
	}
	dir := filepath.Join(bins, m.Key+"-"+executableVariant(t))
	held, err := holdEntry(dir)
	if err != nil {
		return Module{}, err
	}
	if err := linkEntry(dir, m, t, name, stderr); err != nil {
		held.Close()
		return Module{}, err
	}
	m.Executable = filepath.Join(dir, name)
	m.release = closer(held)
	return m, nil
}

// holdEntry makes the executable entry dir, takes a shared lock on it, and
// marks it used. A trim that moved the entry between the open and the lock
// makes it start again, so the lock is always on the directory dir names.
func holdEntry(dir string) (*os.File, error) {
	for range 16 {
		if err := os.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		f, err := os.Open(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue // a trim took it after the mkdir
		}
		if err != nil {
			return nil, err
		}
		if err := lockShared(f); err != nil {
			f.Close()
			return nil, err
		}
		info, same := sameFile(f, dir)
		if !same {
			f.Close()
			continue
		}
		mark(dir, info, time.Now())
		return f, nil
	}
	return nil, fmt.Errorf("build: executable entry %s keeps being removed", dir)
}

// linkEntry runs the go command's link of m into dir/name while it holds the
// entry's link.lock exclusively.
func linkEntry(dir string, m Module, t Toolchain, name string, stderr io.Writer) error {
	lock, err := os.OpenFile(filepath.Join(dir, "link.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockExclusive(lock); err != nil {
		return err
	}
	child := t.Command(m, filepath.Join(dir, name))
	child.Stdout, child.Stderr = stderr, stderr
	if err := child.Run(); err != nil {
		return fmt.Errorf("Go build failed: %w", err)
	}
	return nil
}
