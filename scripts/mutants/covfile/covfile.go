// Package covfile is the coverage store of scripts/mutants (GATE2 GT6) as
// other tools read it: the per-test coverage format, the store of segments
// by key, and the index that says at which commit each segment was
// measured or found to hold (CI-SELECT CS-ROUND). scripts/mutants writes
// the store; scripts/gate's round tier reads it to choose the tests a
// change reaches. The keys stay scripts/mutants' (coverstore.go
// segmentKey): a reader finds a segment through the index, never by
// computing a key of its own.
package covfile

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Version names the store's format and its keys.
const Version = "ergo-mutants-coverage-v2"

// Mode is how the coverage pass runs a suite's tests: every test, without
// -short. A segment of another mode is no record of a full run (CS-ROUND
// round-short-segment-used).
const Mode = "full"

// CacheVariable names the directory of the mutant runner's stores.
const CacheVariable = "ERGO_MUTANTS_CACHE"

// ReadBlock is the block line of a read (scripts/mutants reads.go): no
// block of the cover tool starts at line 0. A test of a read block opened
// the file, which covers every line of it.
const ReadBlock = 0

// Coverage is the per-test coverage of a checkout: which top-level test
// (or example program) runs which block of the covered packages, measured
// once, each test in its own process.
type Coverage struct {
	// Key is the digest of the checkout it was measured on (treeKey).
	Key   string  `json:"key"`
	Tests []*Test `json:"tests"`
	// Files maps a file, relative to the module root, to its blocks that
	// some test runs.
	Files map[string][]Block `json:"files"`
	// Builds is how long each build of the coverage pass took, in seconds
	// (suite or tool name): the base of the mutant pass's build deadlines.
	Builds map[string]float64 `json:"builds"`

	index map[string]int
}

// Test is one test of a suite: its SUITE:NAME, its wall time in the
// coverage pass (the base of its deadline and of the cheapest-first
// order), and whether it failed there. A test that fails on the unmutated
// tree kills nothing.
type Test struct {
	ID      string  `json:"id"`
	Seconds float64 `json:"seconds"`
	Failed  bool    `json:"failed,omitempty"`
	Detail  string  `json:"detail,omitempty"`
}

// Block is a block's first and last line and the tests (indexes into
// Tests) that run it.
type Block struct {
	Start int   `json:"s"`
	End   int   `json:"e"`
	Tests []int `json:"t"`
}

// Reindex rebuilds the index of the tests by id, after Tests changed.
func (c *Coverage) Reindex() {
	c.index = map[string]int{}
	for i, t := range c.Tests {
		c.index[t.ID] = i
	}
}

// Indexed says whether Reindex has run.
func (c *Coverage) Indexed() bool { return c.index != nil }

// Lookup is the test of id, or nil.
func (c *Coverage) Lookup(id string) *Test {
	if i, ok := c.index[id]; ok {
		return c.Tests[i]
	}
	return nil
}

// Covering returns the tests that run a block of file between lines lo and
// hi, the tests that read the file (ReadBlock), and the extra tests that
// exist, the tests that fail on the unmutated tree included (judge refuses
// a mutant one of them covers). A header line (a case or an else if) that
// is in no block takes the tests of the block that ends last before it.
func (c *Coverage) Covering(file string, lo, hi int, header bool, extra []string) map[string]bool {
	out := map[string]bool{}
	var before *Block
	inBlock := false
	for k, b := range c.Files[file] {
		if b.Start == ReadBlock {
			// The tests that read the file cover every line.
			for _, i := range b.Tests {
				out[c.Tests[i].ID] = true
			}
			continue
		}
		if b.Start <= hi && b.End >= lo {
			inBlock = true
			for _, i := range b.Tests {
				out[c.Tests[i].ID] = true
			}
		}
		if b.End < lo && (before == nil || b.End >= before.End) {
			before = &c.Files[file][k]
		}
	}
	// A line in no block is a header the cover tool counts with no block
	// of its own (a case of a tagless switch, an else if): the code that
	// reaches it runs it, so the block that ends last before it stands
	// for it (M1: runtime/v1's case errors.Is(err, ECONNREFUSED) was
	// uncovered).
	if header && !inBlock && before != nil {
		for _, i := range before.Tests {
			out[c.Tests[i].ID] = true
		}
	}
	for _, id := range extra {
		if c.Lookup(id) != nil {
			out[id] = true
		}
	}
	return out
}

// Save writes c to path: a new file, renamed into place, so two runs
// filling the store at once never interleave.
func Save(path string, c *Coverage) error {
	return writeAtomically(path, func(f *os.File) error {
		z := gzip.NewWriter(f)
		err := json.NewEncoder(z).Encode(c)
		if closeErr := z.Close(); err == nil {
			err = closeErr
		}
		return err
	})
}

// Load reads a coverage file.
func Load(path string) (*Coverage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var c Coverage
	if err := json.NewDecoder(z).Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Reindex()
	return &c, nil
}

func writeAtomically(path string, write func(*os.File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.partial")
	if err != nil {
		return err
	}
	err = write(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Store holds coverage segments by key, one gzip JSON file each, and the
// index of the commits they were measured at.
type Store struct{ Dir string }

// CacheDir is the directory of the mutant runner's stores:
// $ERGO_MUTANTS_CACHE, else ergo-mutants in the user's cache directory.
func CacheDir() (string, error) {
	if dir := os.Getenv(CacheVariable); dir != "" {
		return dir, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "ergo-mutants"), nil
}

// Open is the store in CacheDir.
func Open() (Store, error) {
	dir, err := CacheDir()
	if err != nil {
		return Store{}, err
	}
	return Store{filepath.Join(dir, Version)}, nil
}

func (s Store) path(key string) string {
	return filepath.Join(s.Dir, key[:2], key+".json.gz")
}

// Load is the segment stored at key.
func (s Store) Load(key string) (*Coverage, bool) {
	c, err := Load(s.path(key))
	return c, err == nil
}

// Save stores c at key.
func (s Store) Save(key string, c *Coverage) error {
	return Save(s.path(key), c)
}

// Entry says that the segment at Key is a suite's coverage at Commit: the
// runner measured it there, or found its key to hold there, with the
// checkout clean (no change to a tracked file, no untracked file), so a
// reader diffs Commit against its change. Suite, Package, Module and Tags
// are the suite's (scripts/mutants Suite); a Go suite has a Package.
type Entry struct {
	Suite   string `json:"suite"`
	Package string `json:"package,omitempty"`
	Module  string `json:"module,omitempty"`
	Tags    string `json:"tags,omitempty"`
	// Cover are the packages (./DIR) whose blocks the segment records: a
	// block of any other package is in no segment, so an edit there is
	// no edit the segment can place.
	Cover  []string `json:"cover"`
	Key    string   `json:"key"`
	Mode   string   `json:"mode"`
	Commit string   `json:"commit"`
	// GoEnv is GoEnv's answer at the measure: a reader with another
	// toolchain or platform has no record.
	GoEnv    string    `json:"go_env"`
	Recorded time.Time `json:"recorded"`
}

// indexDir is the folder of the entries at commit.
func (s Store) indexDir(commit string) string {
	return filepath.Join(s.Dir, "index", commit)
}

// Record adds e to the index: one file per suite and commit, so two runs
// at one commit never interleave, and the later entry of a suite stands.
func (s Store) Record(e Entry) error {
	if e.Commit == "" || e.Key == "" || e.Suite == "" {
		return errors.New("covfile: an index entry needs a commit, a key and a suite")
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	path := filepath.Join(s.indexDir(e.Commit), url.PathEscape(e.Suite)+".json")
	return writeAtomically(path, func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// At are the entries recorded at commit, by suite name.
func (s Store) At(commit string) ([]Entry, error) {
	files, err := filepath.Glob(filepath.Join(s.indexDir(commit), "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Suite, b.Suite) })
	return out, nil
}

// Commits are the commits the index holds entries at.
func (s Store) Commits() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "index"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// goEnvNames are the go command's settings a segment depends on.
var goEnvNames = []string{"GOVERSION", "GOROOT", "GOFLAGS", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOEXPERIMENT", "CGO_ENABLED", "CC"}

// GoEnv is `go env` of the settings a segment depends on, run with goCmd
// in root under env.
func GoEnv(goCmd, root string, env []string) (string, error) {
	query := exec.Command(goCmd, append([]string{"env"}, goEnvNames...)...)
	query.Dir, query.Env = root, env
	out, err := query.Output()
	if err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	return string(out), nil
}
