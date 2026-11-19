package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// A run survives its own death (GT-BOUND: three lanes had full mutant runs
// killed by server restarts and judged the rest by hand). Each verdict, and
// each named test's run on the unmutated tree (a baseline), is appended to
// the journal of the tree as it lands, and synced; a run of the same tree
// (Config.JournalDir, the tree key and -all name the file) takes the
// verdicts and baselines it holds and judges only the rest. A complete run
// removes its journal. An interrupted verdict is never written.

// journal is the open journal of one tree.
type journal struct {
	path      string
	mu        sync.Mutex
	f         *os.File
	results   map[string]Result
	baselines map[string]*covTest
}

// journalEntry is one line of the journal: a verdict or a baseline.
type journalEntry struct {
	Result   *Result  `json:"result,omitempty"`
	Baseline *covTest `json:"baseline,omitempty"`
}

// resultKey names a row in the journal: its ledger and its name.
func resultKey(ledger, name string) string {
	if abs, err := filepath.Abs(ledger); err == nil {
		ledger = abs
	}
	return ledger + "\x00" + name
}

// openJournal reads the journal of the engine's tree, if any, and opens it
// for appending. With no JournalDir it keeps nothing.
func (e *engine) openJournal() (*journal, error) {
	j := &journal{results: map[string]Result{}, baselines: map[string]*covTest{}}
	if e.cfg.JournalDir == "" {
		return j, nil
	}
	name := e.key
	switch {
	case e.compileOnly:
		name += "-compiles"
	case e.cfg.All:
		name += "-all"
	}
	j.path = filepath.Join(e.cfg.JournalDir, name+".jsonl")
	if err := os.MkdirAll(e.cfg.JournalDir, 0o755); err != nil {
		return nil, err
	}
	if f, err := os.Open(j.path); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1<<20), 16<<20)
		for scanner.Scan() {
			var entry journalEntry
			// A line cut by the death of the run that wrote it is not read.
			if json.Unmarshal(scanner.Bytes(), &entry) != nil {
				continue
			}
			switch {
			case entry.Result != nil && entry.Result.Status != "":
				j.results[resultKey(entry.Result.Ledger, entry.Result.Name)] = *entry.Result
			case entry.Baseline != nil:
				j.baselines[entry.Baseline.ID] = entry.Baseline
			}
		}
		f.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	j.f = f
	return j, nil
}

// add appends an entry and syncs it.
func (j *journal) add(entry journalEntry) error {
	if j.f == nil {
		return nil
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return j.f.Sync()
}

// close closes the journal; complete removes it.
func (j *journal) close(complete bool) error {
	if j.f == nil {
		return nil
	}
	err := j.f.Close()
	if complete && err == nil {
		err = os.Remove(j.path)
	}
	return err
}
