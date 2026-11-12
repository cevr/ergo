// Package toolchain pins the Go release ergo builds with, and fetches,
// verifies and caches it, so a system Go is optional (owner, q136,
// 2026-11-12: "Ergo manages Go").
//
// The release comes the way GOTOOLCHAIN brings one (go help toolchain):
// as the module golang.org/toolchain@v0.0.1-<version>.<goos>-<goarch> from
// GOPROXY, its zip verified against the checksum database (GOSUMDB) before
// a byte of it is used. It is installed under ergo's cache directory, never
// into Go's module cache:
//
//	<cache>/toolchain/<version>.<goos>-<goarch>/release      the GOROOT
//	<cache>/toolchain/<version>.<goos>-<goarch>/lock         the install lock
//	<cache>/toolchain/<version>.<goos>-<goarch>/tmp-*        an install in progress
//
// A release directory appears by one rename, under the lock, after the
// whole tree is written, so it names a complete toolchain or nothing. A
// later artifact built from the release (the IR tier's patched compiler,
// q131) is a sibling keyed by its patch digest, patched-<digest>, in the
// same directory and under the same lock.
package toolchain

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the Go release this ergo builds with. It is the one pin: the
// binary records it, `ergo version` prints it, and the managed toolchain is
// this release.
const Version = "go1.27.4"

// maxZip bounds a toolchain download, as cmd/go bounds a module zip.
const maxZip = 500 << 20

// Toolchain is the go command a build runs.
type Toolchain struct {
	// Mode is "managed", "system" or "path" (ERGO_GO).
	Mode string
	// Go is the go command: a path, or "go" looked up on PATH.
	Go string
	// Root is GOROOT of a managed toolchain, empty otherwise. A build with
	// a Root sets GOROOT to it and GOTOOLCHAIN=local, so neither the
	// user's GOROOT nor a toolchain line can choose another Go.
	Root string
}

// Select chooses the toolchain from ERGO_GO: unset or "managed" is the
// pinned release, installed on first use under cache (Ensure, with the
// GOPROXY and GOSUMDB of getenv); "system" is the go command on PATH, as
// before ergo managed Go; any other value is the path of a go command.
func Select(getenv func(string) string, cache string, log io.Writer) (Toolchain, error) {
	t, _ := Status(getenv, cache)
	if t.Mode == "managed" {
		if _, err := Ensure(Config{Cache: cache, Proxy: getenv("GOPROXY"), SumDB: getenv("GOSUMDB"), Log: log}); err != nil {
			return Toolchain{}, err
		}
	}
	return t, nil
}

// Status is the toolchain Select would choose, without installing it, and
// whether a managed release is in the cache.
func Status(getenv func(string) string, cache string) (Toolchain, bool) {
	switch choice := getenv("ERGO_GO"); choice {
	case "", "managed":
		root := filepath.Join(Config{Cache: cache}.Dir(), "release")
		return Toolchain{Mode: "managed", Go: filepath.Join(root, "bin", "go"+exe()), Root: root}, installed(root)
	case "system":
		return Toolchain{Mode: "system", Go: "go"}, false
	default:
		return Toolchain{Mode: "path", Go: choice}, false
	}
}

// Config is one managed toolchain install.
type Config struct {
	// Cache is ergo's cache directory.
	Cache string
	// Version, GOOS and GOARCH name the release; empty is Version and the
	// host's platform.
	Version, GOOS, GOARCH string
	// Proxy and SumDB are GOPROXY and GOSUMDB; empty is Go's default.
	Proxy, SumDB string
	// Log receives one line when a download starts; nil is none.
	Log io.Writer
	// Client fetches; nil is http.DefaultClient.
	Client *http.Client

	// waiting runs when another install holds the lock (tests).
	waiting func()
	// height is the checksum database's tile height; 0 is GOSUMDB's.
	height int
}

func (c Config) platform() (string, string, string) {
	version, goos, goarch := c.Version, c.GOOS, c.GOARCH
	if version == "" {
		version = Version
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return version, goos, goarch
}

// Dir is the cache directory of the release c names: its release, its
// lock and, later, its patched compilers.
func (c Config) Dir() string {
	version, goos, goarch := c.platform()
	return filepath.Join(c.Cache, "toolchain", version+"."+goos+"-"+goarch)
}

// module is the release's module path and version (go help toolchain).
func (c Config) module() (string, string) {
	version, goos, goarch := c.platform()
	return "golang.org/toolchain", "v0.0.1-" + version + "." + goos + "-" + goarch
}

// Ensure returns the GOROOT of the release c names, downloading,
// verifying and installing it first when the cache does not hold it. With
// a warm cache it reads no network. Two processes that install at once
// download once: the second waits for the first's lock and then finds the
// release.
func Ensure(c Config) (string, error) {
	dir := c.Dir()
	release := filepath.Join(dir, "release")
	if installed(release) {
		return release, nil
	}
	if !filepath.IsAbs(c.Cache) {
		return "", fmt.Errorf("toolchain cache %q is not absolute", c.Cache)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	unlock, err := lock(filepath.Join(dir, "lock"), c.waiting)
	if err != nil {
		return "", err
	}
	defer unlock()
	if installed(release) {
		return release, nil
	}
	// Under the lock no install runs, so what an interrupted one left goes.
	if locks {
		leftovers, _ := filepath.Glob(filepath.Join(dir, "tmp-*"))
		for _, path := range leftovers {
			os.RemoveAll(path)
		}
	}
	if err := c.install(dir, release); err != nil {
		version, goos, goarch := c.platform()
		return "", fmt.Errorf("install %s for %s/%s: %w\n(set ERGO_GO=system to build with the go command on PATH, or ERGO_GO=/path/to/go)", version, goos, goarch, err)
	}
	return release, nil
}

func installed(release string) bool {
	info, err := os.Stat(filepath.Join(release, "bin", "go"+exe()))
	return err == nil && info.Mode().IsRegular()
}

func exe() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// install downloads, verifies and unpacks the release into dir/tmp-*, and
// renames the tree to release.
func (c Config) install(dir, release string) error {
	db, err := parseSumDB(c.SumDB)
	if err != nil {
		return err
	}
	if c.height != 0 {
		db.height = c.height
	}
	path, version := c.module()
	f := fetcher{client: c.Client}
	if f.client == nil {
		f.client = http.DefaultClient
	}
	if c.Log != nil {
		v, goos, goarch := c.platform()
		f.started = func() { fmt.Fprintf(c.Log, "ergo: downloading %s (%s/%s)\n", v, goos, goarch) }
	}
	archive, err := os.CreateTemp(dir, "tmp-*.zip")
	if err != nil {
		return err
	}
	defer func() {
		archive.Close()
		os.Remove(archive.Name())
	}()
	if err := f.download(c.Proxy, path, version, archive); err != nil {
		return err
	}
	want, err := f.lookup(db, path, version)
	if err != nil {
		return err
	}
	size, err := archive.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	z, err := zip.NewReader(archive, size)
	if err != nil {
		return fmt.Errorf("%s@%s: the download is not a whole zip: %w", path, version, err)
	}
	got, err := hashZip(z)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s@%s: checksum mismatch\n\tdownloaded: %s\n\t%s: %s", path, version, got, db.name, want)
	}
	tree, err := os.MkdirTemp(dir, "tmp-")
	if err != nil {
		return err
	}
	if err := extract(z, path+"@"+version+"/", tree); err != nil {
		os.RemoveAll(tree)
		return err
	}
	if !installed(tree) {
		os.RemoveAll(tree)
		return fmt.Errorf("%s@%s holds no bin/go", path, version)
	}
	if err := os.Rename(tree, release); err != nil {
		os.RemoveAll(tree)
		if installed(release) {
			return nil // another install won the rename (no flock)
		}
		return err
	}
	return nil
}

// fetcher reads the module proxy and the checksum database.
type fetcher struct {
	client *http.Client
	// started runs before the first request to a proxy; nil is none.
	started func()
}

// errNotFound is a 404 or 410: the next proxy of a list may have it.
var errNotFound = errors.New("not found")

func (f fetcher) get(url string, w io.Writer, limit int64) error {
	resp, err := f.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return fmt.Errorf("%s: %s: %w", url, resp.Status, errNotFound)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	if n > limit {
		return fmt.Errorf("%s: more than %d bytes", url, limit)
	}
	return nil
}

// lookup returns the h1 hash of path@version that db proves.
func (f fetcher) lookup(db sumDB, path, version string) (string, error) {
	var body strings.Builder
	if err := f.get(db.url+"/lookup/"+path+"@"+version, &body, 1<<20); err != nil {
		return "", fmt.Errorf("checksum database: %w", err)
	}
	id, record, signed, err := parseLookup([]byte(body.String()))
	if err != nil {
		return "", fmt.Errorf("checksum database: %w", err)
	}
	text, err := db.verifyNote(signed)
	if err != nil {
		return "", fmt.Errorf("checksum database: %w", err)
	}
	t, err := parseTree(text)
	if err != nil {
		return "", fmt.Errorf("checksum database: %w", err)
	}
	err = proveRecord(db.height, t, id, record, func(tile string, width int) ([]byte, error) {
		var data strings.Builder
		if err := f.get(db.url+"/"+tile, &data, int64(width)*32); err != nil {
			return nil, err
		}
		return []byte(data.String()), nil
	})
	if err != nil {
		return "", fmt.Errorf("checksum database: %w", err)
	}
	for _, line := range strings.Split(string(record), "\n") {
		if hash, ok := strings.CutPrefix(line, path+" "+version+" "); ok && strings.HasPrefix(hash, "h1:") {
			return hash, nil
		}
	}
	return "", fmt.Errorf("checksum database: the record of %s@%s holds no h1 hash of its zip", path, version)
}

// download writes the zip of path@version from the first proxy of the
// GOPROXY list that has it. A proxy after "," is tried when the one before
// has no such module (404, 410); after "|", on any error. "off" ends the
// list offline; "direct" is skipped, since a toolchain has no repository
// to fetch from.
func (f fetcher) download(proxies, path, version string, w *os.File) error {
	if proxies == "" {
		proxies = "https://proxy.golang.org,direct"
	}
	var last error
	for proxies != "" {
		i := strings.IndexAny(proxies, ",|")
		entry, fallThrough := proxies, false
		if i >= 0 {
			entry, fallThrough, proxies = proxies[:i], proxies[i] == '|', proxies[i+1:]
		} else {
			proxies = ""
		}
		switch entry = strings.TrimSpace(entry); entry {
		case "off":
			return errors.New("GOPROXY=off: the toolchain is not in the cache and downloads are off")
		case "direct", "":
			last = fmt.Errorf("GOPROXY: %s@%s comes only from a module proxy", path, version)
			continue
		}
		if _, err := w.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := w.Truncate(0); err != nil {
			return err
		}
		if f.started != nil {
			f.started()
			f.started = nil
		}
		last = f.get(strings.TrimSuffix(entry, "/")+"/"+path+"/@v/"+version+".zip", w, maxZip)
		if last == nil || !(fallThrough || errors.Is(last, errNotFound)) {
			return last
		}
	}
	if last == nil {
		last = errors.New("GOPROXY lists no proxy")
	}
	return last
}
