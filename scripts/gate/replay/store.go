package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Store is the gate's store under one cache directory, shared by every
// worktree:
//
//	<cache>/ergo-gate-v4/objects/<sha[:2]>/<sha>   a step's output, named by its SHA-256
//	<cache>/ergo-gate-v4/whole/<key[:2]>/<key>.json the whole-gate record W(key)
//
// Objects are written once: a temporary file linked into place. A record is
// written last, by one atomic rename. Any process may delete or damage
// either at any time (a manual clean, a future eviction, a withdrawal): a
// replay verifies every object through the descriptor it prints from, so
// that costs a rerun, never a stale replay.
type Store struct {
	// Dir is <cache>/ergo-gate-v4.
	Dir string
}

// Open is the store under the gate cache directory cache.
func Open(cache string) Store {
	return Store{Dir: filepath.Join(cache, Version)}
}

// Record is the whole-gate record W(K): the passed run at key K.
type Record struct {
	Version  string    `json:"version"`
	Key      string    `json:"key"`
	Head     string    `json:"head"`
	Tree     string    `json:"tree"`
	Clean    bool      `json:"clean"`
	Recorded time.Time `json:"recorded"`
	Steps    []Step    `json:"steps"`
}

// Step is one step of a record.
type Step struct {
	Name string `json:"name"`
	// Object names the step's printed output in the store: its SHA-256.
	Object string `json:"object"`
	Bytes  int64  `json:"bytes"`
	// Outcome is "ran" or "cached" (replayed from a step receipt) when the
	// run was recorded.
	Outcome string `json:"outcome"`
	// Seconds is how long the step ran, or the receipt's time when cached.
	Seconds float64   `json:"seconds"`
	Passed  time.Time `json:"passed"`
}

func (s Store) objectPath(sum string) string {
	return filepath.Join(s.Dir, "objects", sum[:2], sum)
}

// RecordPath is where W(key) lives.
func (s Store) RecordPath(key string) string {
	return filepath.Join(s.Dir, "whole", key[:2], key+".json")
}

// validName reports whether name is a lowercase SHA-256 in hex, so a record
// cannot name a path outside the store.
func validName(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, c := range name {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Object is a step's output while the gate prints it: a temporary file in
// the store that becomes a named object at Commit.
type Object struct {
	store Store
	file  *os.File
	hash  hash.Hash
	size  int64
	err   error
}

// NewObject starts an object: a temporary file in the store's objects
// directory.
func (s Store) NewObject() (*Object, error) {
	dir := filepath.Join(s.Dir, "objects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(dir, ".write-")
	if err != nil {
		return nil, err
	}
	return &Object{store: s, file: file, hash: sha256.New()}, nil
}

// File is the object's temporary file, for a check of where it is.
func (o *Object) File() *os.File { return o.file }

// Write adds to the object. It never fails its caller, which writes the
// same bytes to the log: a failed write only keeps the object from Commit.
func (o *Object) Write(p []byte) (int, error) {
	if o.err == nil {
		n, err := o.file.Write(p)
		o.hash.Write(p[:n])
		o.size += int64(n)
		o.err = err
	}
	return len(p), nil
}

// Abort drops the object.
func (o *Object) Abort() {
	_ = o.file.Close()
	_ = os.Remove(o.file.Name())
}

// commit names the object by its SHA-256. An object of that name that
// verifies is kept (write once); one that does not, damaged by a sweeper, is
// replaced by a rename, which a reader that holds the old file never sees.
func (o *Object) commit() (sum string, size int64, err error) {
	temporary := o.file.Name()
	defer os.Remove(temporary)
	closeErr := o.file.Close()
	if err := errors.Join(o.err, closeErr); err != nil {
		return "", 0, fmt.Errorf("write object: %w", err)
	}
	sum = hex.EncodeToString(o.hash.Sum(nil))
	final := o.store.objectPath(sum)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", 0, err
	}
	err = os.Link(temporary, final)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrExist):
		if verifyFile(final, sum, o.size) == nil {
			break
		}
		if err := os.Rename(temporary, final); err != nil {
			return "", 0, err
		}
	default:
		return "", 0, err
	}
	return sum, o.size, nil
}

// verifyFile checks a stored object's size and digest.
func verifyFile(name, sum string, size int64) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return verify(file, io.Discard, sum, size)
}

// verify reads file to its end into copy, and checks that it holds exactly
// size bytes whose SHA-256 is sum.
func verify(file *os.File, copy io.Writer, sum string, size int64) error {
	digest := sha256.New()
	read, err := io.CopyBuffer(io.MultiWriter(copy, digest), io.LimitReader(onlyReader{file}, size+1), make([]byte, 32<<10))
	if err != nil {
		return err
	}
	if read != size || hex.EncodeToString(digest.Sum(nil)) != sum {
		return fmt.Errorf("object %s does not verify (%d bytes, want %d)", sum[:12], read, size)
	}
	return nil
}

// onlyReader hides a file's WriterTo, so io.CopyBuffer uses its buffer.
type onlyReader struct{ io.Reader }

// write stores a record under its key by one atomic rename.
func (s Store) write(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	name := s.RecordPath(record.Key)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".write-")
	if err != nil {
		return err
	}
	_, writeErr := temporary.Write(append(data, '\n'))
	closeErr := temporary.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(temporary.Name())
		return err
	}
	if err := os.Rename(temporary.Name(), name); err != nil {
		_ = os.Remove(temporary.Name())
		return err
	}
	return nil
}

// Withdraw removes W(key): a failed run at key K, or a merge tier that found
// a step whose uncached verdict differs, withdraws the record at K.
func (s Store) Withdraw(key string) error {
	err := os.Remove(s.RecordPath(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// maxRecordSize bounds what Load reads of a record.
const maxRecordSize = 1 << 20

// Load reads W(key), without its objects. It fails when there is no record,
// or the record does not parse or names another key.
func (s Store) Load(key string) (*Record, error) {
	file, err := os.Open(s.RecordPath(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no record for key %s", key[:12])
		}
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRecordSize+1))
	if err != nil {
		return nil, err
	}
	var record Record
	if len(data) > maxRecordSize || json.Unmarshal(data, &record) != nil || record.Version != Version || record.Key != key || len(record.Steps) == 0 {
		return nil, fmt.Errorf("the record for key %s does not parse", key[:12])
	}
	for _, step := range record.Steps {
		if !validName(step.Object) || step.Bytes < 0 {
			return nil, fmt.Errorf("the record for key %s names an invalid object", key[:12])
		}
	}
	return &record, nil
}

// Replay is a verified whole-gate record: each step's output in a private
// file the gate prints from.
type Replay struct {
	Record  *Record
	Outputs []*os.File
}

// Close closes the outputs.
func (r *Replay) Close() {
	for _, output := range r.Outputs {
		if output != nil {
			_ = output.Close()
		}
	}
}

// Lookup opens W(key) and every object it names, and verifies each object's
// size and digest through the descriptor it opened, copying it as it reads
// into a private file from spool (an unlinked file only the gate holds). The
// gate prints from those private files, so no later unlink, rename or
// in-place write of an object reaches what it prints, and it never opens an
// object by name again. A missing or damaged object fails the lookup: the
// gate then runs.
func (s Store) Lookup(key string, spool func() (*os.File, error)) (*Replay, error) {
	record, err := s.Load(key)
	if err != nil {
		return nil, err
	}
	replay := &Replay{Record: record}
	for _, step := range record.Steps {
		output, err := s.open(step, spool)
		if err != nil {
			replay.Close()
			return nil, fmt.Errorf("step %q: %w", step.Name, err)
		}
		replay.Outputs = append(replay.Outputs, output)
	}
	return replay, nil
}

func (s Store) open(step Step, spool func() (*os.File, error)) (*os.File, error) {
	object, err := os.Open(s.objectPath(step.Object))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("object %s is missing", step.Object[:12])
		}
		return nil, err
	}
	defer object.Close()
	private, err := spool()
	if err != nil {
		return nil, err
	}
	if err := verify(object, private, step.Object, step.Bytes); err != nil {
		_ = private.Close()
		return nil, err
	}
	if _, err := private.Seek(0, io.SeekStart); err != nil {
		_ = private.Close()
		return nil, err
	}
	return private, nil
}

// Print writes a replay: for each step a line that says it replayed, from
// when and how long it took, and its recorded output, then the closing line.
// The lines are fixed (docs/tooling.md).
func (r *Replay) Print(out io.Writer) error {
	record := r.Record
	for i, step := range record.Steps {
		if _, err := fmt.Fprintf(out, "== %s: ok, replayed (passed in %.1fs at %s)\n", step.Name, step.Seconds, step.Passed.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if _, err := io.CopyBuffer(out, onlyReader{r.Outputs[i]}, make([]byte, 32<<10)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "gate: replayed %d steps (key %s, recorded %s at head %s)\n", len(record.Steps), record.Key[:12], record.Recorded.UTC().Format(time.RFC3339), record.Head)
	return err
}
