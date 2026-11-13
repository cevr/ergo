package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The verdict cache (gate-speed GS1). A program's legs (fmt, build, test,
// check, explore and its cases) give the same verdict and print the same
// lines whenever everything they read is the same: the program's folder,
// the compiler (bin/ergo, which embeds the checker, the lowering, the std
// snapshot and the runtime that ergo build emits), the runner itself (its
// law check links the checker's front end), the Go toolchain the compiler
// builds with, the environment and the runner's settings. With -cache DIR
// the runner keys each program on all of them; a passed program whose key
// is recorded is replayed (its recorded lines, then a line saying so) and
// not run, and any other program runs. Only a pass is recorded: a failure
// always runs again. A key that cannot be computed is no key: the program
// runs, and a line says why it was not cached (fail closed).
//
// Programs whose result depends on what the key cannot name are never
// cached: a ts program reads the Bun cache, a net program the network and
// a cc program the C toolchain (uncachedTags).

// cacheVariable names the cache directory when -cache is not given; the
// gate sets it for its examples step (scripts/gate, examplesStep).
const cacheVariable = "ERGO_EXAMPLES_CACHE"

// cacheVersion changes when what an entry holds or what the key covers
// changes, so an older entry is never read as a newer one.
const cacheVersion = "ergo-examples-cache v1"

// cacheKeep is how many entries of one program the cache keeps: the newest.
const cacheKeep = 8

// uncachedTags are the tags of programs that are never cached: their
// verdict reads inputs outside the key.
var uncachedTags = []string{"ts", "net", "cc", "tty", "gui"}

// goSettings are the go env values that change what the compiler's go
// builds produce.
var goSettings = []string{"GOVERSION", "GOROOT", "GOOS", "GOARCH", "GOFLAGS", "GOEXPERIMENT", "GOAMD64", "GOARM64", "CGO_ENABLED", "CC", "CGO_CFLAGS", "CGO_LDFLAGS", "GOTOOLCHAIN"}

// cacheEntry is one recorded pass.
type cacheEntry struct {
	Version string `json:"version"`
	Program string `json:"program"`
	Key     string `json:"key"`
	Output  string `json:"output"`
	Cases   int    `json:"cases"`
}

// verdictCache is the run's cache: its directory, and the part of the key
// every program shares (or why there is none).
type verdictCache struct {
	dir    string
	common string
	reason string
	// hits and misses count the run's programs replayed and run.
	hits, misses int
}

// openCache prepares the cache of a run, or returns nil without a
// directory. The common key part is computed once: a failure there leaves
// every program uncached with the reason.
func openCache(dir string, cfg *config) *verdictCache {
	if dir == "" {
		return nil
	}
	c := &verdictCache{dir: dir}
	common, err := commonKey(cfg)
	if err != nil {
		c.reason = err.Error()
	} else {
		c.common = common
	}
	return c
}

// commonKey hashes what every program's verdict reads beside its folder.
func commonKey(cfg *config) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", cacheVersion)
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("the runner's own binary: %w", err)
	}
	for _, f := range []struct{ what, path string }{{"runner", self}, {"ergo", cfg.ergo}} {
		sum, err := fileDigest(f.path)
		if err != nil {
			return "", fmt.Errorf("digest of the %s %s: %w", f.what, f.path, err)
		}
		fmt.Fprintf(h, "%s %s\n", f.what, sum)
	}
	query := exec.Command("go", append([]string{"env", "-json"}, goSettings...)...)
	query.Dir = cfg.root
	out, err := query.Output()
	if err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	fmt.Fprintf(h, "go %s\n", out)
	env := slices.Clone(os.Environ())
	sort.Strings(env)
	for _, kv := range env {
		fmt.Fprintf(h, "env %q\n", kv)
	}
	fmt.Fprintf(h, "settings timeout=%s budget=%s memory=%d root=%s\n", cfg.timeout, cfg.budget, cfg.memory, cfg.root)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileDigest is the SHA-256 of a file's bytes, through links.
func fileDigest(path string) (string, error) {
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

// programKey is the key of one program: the common part and every entry of
// its folder (name, kind, mode and bytes; a link by its target), so an
// untracked file there counts too. "" and the reason when there is none.
func (c *verdictCache) programKey(p *program) (string, string) {
	if c.reason != "" {
		return "", c.reason
	}
	for _, tag := range p.tags {
		if slices.Contains(uncachedTags, tag) {
			return "", "its tag " + tag + " reads inputs outside the key"
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "common %s\nprogram %s\n", c.common, p.name)
	err := filepath.WalkDir(p.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(p.dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "link %q %q\n", filepath.ToSlash(rel), target)
		case d.IsDir():
			fmt.Fprintf(h, "dir %q %o\n", filepath.ToSlash(rel), info.Mode().Perm())
		case d.Type().IsRegular():
			sum, err := fileDigest(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "file %q %o %s\n", filepath.ToSlash(rel), info.Mode().Perm(), sum)
		default:
			return fmt.Errorf("%s is neither a file, a folder nor a link", path)
		}
		return nil
	})
	if err != nil {
		return "", err.Error()
	}
	return hex.EncodeToString(h.Sum(nil)), ""
}

// entryPath is where the pass of a program at key is recorded.
func (c *verdictCache) entryPath(name, key string) string {
	return filepath.Join(c.dir, strings.ReplaceAll(name, "/", "_")+"-"+key[:32]+".json")
}

// lookup returns the recorded pass of p at key.
func (c *verdictCache) lookup(p *program, key string) (*cacheEntry, bool) {
	data, err := os.ReadFile(c.entryPath(p.name, key))
	if err != nil {
		return nil, false
	}
	var e cacheEntry
	if json.Unmarshal(data, &e) != nil || e.Version != cacheVersion || e.Key != key || e.Program != p.name {
		return nil, false
	}
	return &e, true
}

// record writes the pass of p at key, whole or not at all (a rename), and
// keeps the program's newest cacheKeep entries.
func (c *verdictCache) record(p *program, key string, output []byte, cases int) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cacheEntry{Version: cacheVersion, Program: p.name, Key: key, Output: string(output), Cases: cases})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, ".entry-")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.entryPath(p.name, key)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return c.prune(p.name)
}

// prune removes all but the newest cacheKeep entries of a program.
func (c *verdictCache) prune(name string) error {
	matches, err := filepath.Glob(filepath.Join(c.dir, strings.ReplaceAll(name, "/", "_")+"-*.json"))
	if err != nil || len(matches) <= cacheKeep {
		return err
	}
	type aged struct {
		path string
		mod  int64
	}
	var entries []aged
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil {
			entries = append(entries, aged{m, info.ModTime().UnixNano()})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod > entries[j].mod })
	var errs []error
	for _, e := range entries[min(cacheKeep, len(entries)):] {
		errs = append(errs, os.Remove(e.path))
	}
	return errors.Join(errs...)
}

// runCached runs p through the cache: a recorded pass is replayed, and a
// pass of a run is recorded.
func (r *runner) runCached(p *program) {
	c := r.cache
	if c == nil {
		r.runProgram(p)
		return
	}
	key, why := c.programKey(p)
	if key == "" {
		fmt.Fprintf(r.out, "examples: %s: not cached: %s\n", p.name, why)
		c.misses++
		r.runProgram(p)
		return
	}
	if e, ok := c.lookup(p, key); ok {
		io.WriteString(r.out, e.Output)
		r.cases += e.Cases
		c.hits++
		fmt.Fprintf(r.out, "ok %s: cached (inputs %s)\n", p.name, key[:12])
		return
	}
	c.misses++
	var captured bytes.Buffer
	out, failures, cases := r.out, len(r.failures), r.cases
	r.out = io.MultiWriter(out, &captured)
	r.runProgram(p)
	r.out = out
	if len(r.failures) == failures {
		if err := c.record(p, key, captured.Bytes(), r.cases-cases); err != nil {
			fmt.Fprintf(r.out, "examples: %s: pass not recorded: %v\n", p.name, err)
		}
	}
}
