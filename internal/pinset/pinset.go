// Package pinset ships what a pin compiles as a download (compile-speed
// plan §3.3, CS3): the Go standard library's archives, the Ergo runtime's
// archives and the patched compile, each a set built once per pin and per
// OS/arch, named in toolchain/pin/lock by sha256, fetched the way the
// toolchain is fetched and verified before a byte of it is used.
//
// A pin is the Go release, the IR patch digest (toolchain/ir/lock) and the
// flags a build compiles with. A set's name keys all of them and its
// target, so a set is never used for another pin or target (Key.Name).
//
// A set is a tar of an index and the blobs it names:
//
//	index          one line per entry, sorted: NAME DIGEST EXPORT
//	a/DIGEST       an entry's bytes, named by their sha256
//
// NAME is a standard import path (a std set), the hex key of a compile
// (a runtime set) or a tool's name (a tools set); EXPORT is the digest of
// a package archive's export data, or "-". The tar is written with fixed
// headers in sorted order, so equal entries make equal bytes; the lock's
// digest is the tar's, and the file served is the tar compressed with
// gzip (NAME.tar.gz), so the compressor's version is no input.
//
// Installed, a set is the directory DIR/NAME holding the index and blobs.
// It appears by one rename after it is verified, so it names a whole
// verified set or nothing. A set that cannot be fetched, or whose digest
// is not the lock's, is refused with one line on the log, and the caller
// builds what it would have held once, into its own store.
package pinset

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// schema versions a set's name: a change of the format or of what a name
// keys renames every set.
const schema = "ergo pin set 1"

// maxSet bounds a set's download, compressed and expanded.
const maxSet = 2 << 30

// Key is what a set holds: its kind and the pin and target it was built
// for.
type Key struct {
	// Kind is "std", "runtime" or "tools".
	Kind string
	// Release is the Go release, such as go1.27.4.
	Release string
	// Patch is the sha256 of the IR compiler patch the compile carries
	// (toolchain/ir/lock); empty is the stock compile.
	Patch string
	// GOOS and GOARCH are the target: the objects' platform for std and
	// runtime, the host the tool runs on for tools.
	GOOS, GOARCH string
	// Level is the target's arch level (GOAMD64, GOARM64), or empty.
	Level string
	// Flags are the flags of the compiles, such as -trimpath, -shared and
	// the build tags.
	Flags string
}

// Name is the set's name: readable fields, then a digest of every field
// of the key. Two keys that differ in any field have different names.
func (k Key) Name() string {
	var b bytes.Buffer
	for _, f := range [][2]string{{"schema", schema}, {"kind", k.Kind}, {"release", k.Release}, {"patch", k.Patch}, {"goos", k.GOOS}, {"goarch", k.GOARCH}, {"level", k.Level}, {"flags", k.Flags}} {
		fmt.Fprintf(&b, "%d:%s%d:%s", len(f[0]), f[0], len(f[1]), f[1])
	}
	sum := sha256.Sum256(b.Bytes())
	name := []string{k.Kind, k.Release, k.GOOS, k.GOARCH}
	if k.Level != "" {
		name = append(name, k.Level)
	}
	return strings.Join(name, "-") + "-" + hex.EncodeToString(sum[:])[:16]
}

// Lock is toolchain/pin/lock: the sha256 of every set a release ships.
type Lock struct {
	// Go and Patch are the lock's header: the release and the IR patch
	// digest the sets were built with.
	Go, Patch string
	// Sets maps a set's name to the hex sha256 of its tar.
	Sets map[string]string
}

// ParseLock reads a lock: "#" comments, "go RELEASE", "patch sha256:HEX"
// and one "set NAME sha256:HEX" line per set.
func ParseLock(data []byte) (Lock, error) {
	l := Lock{Sets: map[string]string{}}
	for i, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 0 || strings.HasPrefix(f[0], "#"):
		case len(f) == 2 && f[0] == "go":
			l.Go = f[1]
		case len(f) == 2 && f[0] == "patch" && strings.HasPrefix(f[1], "sha256:"):
			l.Patch = strings.TrimPrefix(f[1], "sha256:")
		case len(f) == 3 && f[0] == "set" && strings.HasPrefix(f[2], "sha256:"):
			l.Sets[f[1]] = strings.TrimPrefix(f[2], "sha256:")
		default:
			return Lock{}, fmt.Errorf("pin lock line %d: %q", i+1, line)
		}
	}
	return l, nil
}

// Entry is one entry of a set.
type Entry struct {
	Name   string
	Digest string // sha256 of the bytes
	Export string // export data digest of a package archive, or "-"
}

// Set is an installed set.
type Set struct {
	Dir     string
	Entries map[string]Entry
}

// Path is the file holding an entry's bytes.
func (s *Set) Path(e Entry) string { return filepath.Join(s.Dir, "a", e.Digest) }

// Source is where sets come from: the installed sets, the URL missing
// ones are fetched from, and the lock that names them.
type Source struct {
	// Dir holds the installed sets; empty is no sets at all.
	Dir string
	// URL is the base a missing set NAME is fetched from as
	// URL/NAME.tar.gz (http, https or file); empty fetches nothing.
	URL string
	// Lock names the sets that may be fetched.
	Lock Lock
	// Log receives one line for a set that was refused or not fetched.
	Log io.Writer
}

// Open returns the installed set name, fetching and installing it first
// when the lock names it and a URL is set. It returns nil, quietly, when
// the lock names no such set or there is no URL; nil with one line on the
// log when the fetch fails or the set's digest is not the lock's.
func (s Source) Open(name, what string) *Set {
	if s.Dir == "" {
		return nil
	}
	dir := filepath.Join(s.Dir, name)
	if set, err := read(dir); err == nil {
		return set
	}
	want, locked := s.Lock.Sets[name]
	if !locked || s.URL == "" {
		return nil
	}
	err := s.install(name, want)
	if err == nil {
		set, rerr := read(dir)
		if rerr == nil {
			return set
		}
		err = rerr
	}
	if s.Log != nil {
		fmt.Fprintf(s.Log, "ergo: pin archives %s refused: %v; building %s once instead\n", name, err, what)
	}
	return nil
}

// read reads an installed set.
func read(dir string) (*Set, error) {
	data, err := os.ReadFile(filepath.Join(dir, "index"))
	if err != nil {
		return nil, err
	}
	entries, err := parseIndex(data)
	if err != nil {
		return nil, err
	}
	return &Set{Dir: dir, Entries: entries}, nil
}

func parseIndex(data []byte) (map[string]Entry, error) {
	entries := map[string]Entry{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || !isDigest(f[1]) {
			return nil, fmt.Errorf("bad index line %q", line)
		}
		entries[f[0]] = Entry{Name: f[0], Digest: f[1], Export: f[2]}
	}
	return entries, nil
}

func isDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// install fetches NAME.tar.gz, expands it into a new directory while
// hashing the tar, and renames the directory into place only when the
// digest is want.
func (s Source) install(name, want string) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	body, err := s.fetch(name + ".tar.gz")
	if err != nil {
		return err
	}
	defer body.Close()
	tmp, err := os.MkdirTemp(s.Dir, "tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	zr, err := gzip.NewReader(io.LimitReader(body, maxSet))
	if err != nil {
		return fmt.Errorf("%s.tar.gz: %w", name, err)
	}
	h := sha256.New()
	if err := expand(io.TeeReader(io.LimitReader(zr, maxSet), h), tmp); err != nil {
		return fmt.Errorf("%s.tar.gz: %w", name, err)
	}
	// The rest of the tar (its end blocks) is hashed too.
	if _, err := io.Copy(h, io.LimitReader(zr, maxSet)); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("its sha256 is %s, toolchain/pin/lock says %s", got, want)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.Dir, name)); err != nil {
		if _, rerr := read(filepath.Join(s.Dir, name)); rerr == nil {
			return nil // another process installed it first
		}
		return err
	}
	return nil
}

// fetch opens URL/file.
func (s Source) fetch(file string) (io.ReadCloser, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		return os.Open(filepath.Join(filepath.FromSlash(u.Path), file))
	case "http", "https":
		resp, err := http.Get(strings.TrimSuffix(s.URL, "/") + "/" + file)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s", file, resp.Status)
		}
		return resp.Body, nil
	}
	return nil, fmt.Errorf("pin archive URL %q is not http, https or file", s.URL)
}

// expand writes a set's tar into dir: an index and blobs named by their
// digest, each checked against its name.
func expand(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	if err := os.MkdirAll(filepath.Join(dir, "a"), 0o755); err != nil {
		return err
	}
	sawIndex := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		digest, blob := strings.CutPrefix(hdr.Name, "a/")
		switch {
		case hdr.Typeflag != tar.TypeReg:
			return fmt.Errorf("entry %s is not a file", hdr.Name)
		case hdr.Name == "index":
			sawIndex = true
		case !blob || !isDigest(digest):
			return fmt.Errorf("unexpected entry %s", hdr.Name)
		}
		f, err := os.OpenFile(filepath.Join(dir, filepath.FromSlash(hdr.Name)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(f, h), tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		if blob && hex.EncodeToString(h.Sum(nil)) != digest {
			return fmt.Errorf("entry %s does not hold its digest", hdr.Name)
		}
	}
	if !sawIndex {
		return errors.New("no index")
	}
	data, err := os.ReadFile(filepath.Join(dir, "index"))
	if err != nil {
		return err
	}
	entries, err := parseIndex(data)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(dir, "a", e.Digest)); err != nil {
			return fmt.Errorf("index names %s, which the set does not hold", e.Digest)
		}
	}
	return nil
}

// Produced is one set written by Write.
type Produced struct {
	Name   string
	Digest string // the tar's sha256, the lock's
	File   string // NAME.tar.gz
}

// LockLine is the set's line in toolchain/pin/lock.
func (p Produced) LockLine() string { return "set " + p.Name + " sha256:" + p.Digest }

// Write writes the set name of entries, whose bytes are in the files
// paths[entry name], to dir/NAME.tar.gz.
func Write(dir, name string, entries []Entry, paths map[string]string) (Produced, error) {
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	var index strings.Builder
	blobs := map[string]string{} // digest → a file holding it
	for _, e := range entries {
		fmt.Fprintf(&index, "%s %s %s\n", e.Name, e.Digest, e.Export)
		blobs[e.Digest] = paths[e.Name]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Produced{}, err
	}
	p := Produced{Name: name, File: filepath.Join(dir, name+".tar.gz")}
	out, err := os.CreateTemp(dir, "tmp-")
	if err != nil {
		return Produced{}, err
	}
	defer os.Remove(out.Name())
	zw := gzip.NewWriter(out)
	h := sha256.New()
	bw := bufio.NewWriter(io.MultiWriter(zw, h))
	tw := tar.NewWriter(bw)
	add := func(name string, r io.Reader, size int64) error {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := io.Copy(tw, r)
		return err
	}
	if err := add("index", strings.NewReader(index.String()), int64(index.Len())); err != nil {
		return Produced{}, err
	}
	for _, digest := range slices.Sorted(func(yield func(string) bool) {
		for d := range blobs {
			if !yield(d) {
				return
			}
		}
	}) {
		f, err := os.Open(blobs[digest])
		if err != nil {
			return Produced{}, err
		}
		info, err := f.Stat()
		if err == nil {
			err = add("a/"+digest, f, info.Size())
		}
		f.Close()
		if err != nil {
			return Produced{}, err
		}
	}
	for _, err := range []error{tw.Close(), bw.Flush(), zw.Close(), out.Close()} {
		if err != nil {
			return Produced{}, err
		}
	}
	p.Digest = hex.EncodeToString(h.Sum(nil))
	if err := os.Chmod(out.Name(), 0o644); err != nil { // CreateTemp's 0600; a set is served
		return Produced{}, err
	}
	return p, os.Rename(out.Name(), p.File)
}

// FileDigest is the sha256 of a file's bytes.
func FileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
