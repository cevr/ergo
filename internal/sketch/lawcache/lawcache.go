// Package lawcache is the store of law verdicts (v1 audit 3, F7; owner:
// no double work). A law's verdict is a function of its inputs: the
// tokens of every declaration the law, its providers and its sites read
// (lower.TargetDigest, as the ledger's LawDigest), the seed, the budget
// of states, and the compiler that runs it (its evaluator, its lowering
// and its prover). The store keys each verdict by all of them, so
// check-time evaluation, ergo test and the ledger's check run each run a
// law once per key, and skip it with "(cached)" while the key holds. A
// stale key is a new key: it misses, and the law runs again.
//
// Only a verdict that held is stored: a refuted law, a defect, an
// exhausted budget or a skip is never cached, so it runs (and fails)
// every time. A store is in process memory and, when it has a root and a
// compiler of content identity, on disk under the emission cache's root
// (Root/laws), one small file per key, written by one rename, marked used
// when read, and bounded by the emission cache's trim (build's
// lawtrim.go: by age, and past a count, least recently used). A compiler
// identity that is process-scoped (go run, go test) keeps the store in
// memory: another process's compiler may differ.
//
// Prior art: go test caches a package's passing result keyed by its
// inputs and prints "(cached)"; Bazel caches test actions by their input
// digests; Lean keeps each declaration's checked proof in its .olean;
// Dafny caches verification per declaration. Like go test, the store
// caches a pass only, and like Bazel, the key is a digest of the inputs,
// never a time.
package lawcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Version names the store's key and text encoding: a change of either
// makes every entry a miss.
const Version = "ergo-lawcache/2"

// Store is a verdict store. The zero value is not usable; use Open or
// Memory. A nil *Store stores nothing and finds nothing.
type Store struct {
	compiler string
	dir      string // "" keeps the store in memory only

	mu    sync.Mutex
	mem   map[string]string
	runs  map[string]int
	wrote int
}

var (
	openMu sync.Mutex
	opened = map[[2]string]*Store{}
)

// Open is the store of the cache root and the compiler identity: one
// store per pair in a process, so check-time evaluation, the test build
// and the ledger of one process share its memory. root "" or a
// process-scoped compiler ("process:…") keeps it in memory; compiler ""
// gives nil, a store that keeps nothing.
func Open(root, compiler string) *Store {
	if compiler == "" {
		return nil
	}
	openMu.Lock()
	defer openMu.Unlock()
	k := [2]string{root, compiler}
	if s := opened[k]; s != nil {
		return s
	}
	s := &Store{compiler: compiler, mem: map[string]string{}, runs: map[string]int{}}
	if root != "" && !strings.HasPrefix(compiler, "process:") {
		s.dir = filepath.Join(root, "laws", "v1")
	}
	opened[k] = s
	return s
}

// Memory is a fresh store in memory, of the compiler identity compiler.
func Memory(compiler string) *Store {
	return &Store{compiler: compiler, mem: map[string]string{}, runs: map[string]int{}}
}

// Key is the key of a verdict whose inputs are parts, each
// length-prefixed, so no two lists of parts share a key.
func Key(parts ...string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:%s", len(Version), Version)
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// file is the key's entry: the key with the compiler, hashed again, so an
// entry of another compiler is another file.
func (s *Store) file(key string) (name, path string) {
	name = Key(s.compiler, key)
	if s.dir == "" {
		return name, ""
	}
	return name, filepath.Join(s.dir, name[:2], name)
}

// Get is the verdict text stored under key, and whether there is one.
func (s *Store) Get(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	name, path := s.file(key)
	s.mu.Lock()
	text, ok := s.mem[name]
	s.mu.Unlock()
	if ok || path == "" {
		return text, ok
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || failed(string(data)) {
		return "", false
	}
	text = string(data)
	s.mu.Lock()
	s.mem[name] = text
	s.mu.Unlock()
	markUsed(path, time.Now())
	return text, true
}

// markInterval is the least time between two use marks of an entry, the
// emission cache's (build's trim.go): the trim removes the least recently
// used entries first, by their modification time.
const markInterval = time.Hour

// markUsed sets the use mark of the entry at path to now, unless its
// last mark is less than markInterval old. A failed mark costs only the
// entry's age.
func markUsed(path string, now time.Time) {
	if info, err := os.Stat(path); err == nil && now.Sub(info.ModTime()) >= markInterval {
		_ = os.Chtimes(path, now, now)
	}
}

// Wrote is how many entries this process wrote to the store's disk: the
// command that wrote any asks the emission cache's trim to bound them
// (build.Cache.LawsWritten).
func (s *Store) Wrote() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

// Put stores text, a verdict that held, under key, and counts one run of
// key (Runs). A text with a FAIL line is a failure, which Put never
// stores. A failed disk write leaves the verdict in memory only: the
// next process runs the law again.
func (s *Store) Put(key, text string) {
	if s == nil {
		return
	}
	name, path := s.file(key)
	s.mu.Lock()
	s.runs[name]++
	if text == "" || failed(text) {
		s.mu.Unlock()
		return
	}
	s.mem[name] = text
	s.mu.Unlock()
	if path != "" && writeAtomic(path, []byte(text)) == nil {
		s.mu.Lock()
		s.wrote++
		s.mu.Unlock()
	}
}

// Runs is how many times Put was called for key in this process: the
// count of the law's runs, which the counted guard holds at one.
func (s *Store) Runs(key string) int {
	if s == nil {
		return 0
	}
	name, _ := s.file(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[name]
}

// failed reports whether text holds a failure line.
func failed(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "FAIL") {
			return true
		}
	}
	return false
}

// writeAtomic writes data to path by a rename, so a reader sees no entry
// or the whole one.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	part, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	_, werr := part.Write(data)
	if cerr := part.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(part.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(part.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(part.Name())
	}
	return werr
}
