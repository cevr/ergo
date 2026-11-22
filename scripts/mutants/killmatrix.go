package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// The kill matrix (CI-SELECT CS-ALPHA): for each row of a ledger, every
// test that kills it, not the first. It is derived (`testdata/kills.json`
// beside the ledger, merge class derived in .gitattributes): -alpha
// writes it and reads it back as its cache, so a row whose key is
// unchanged is not judged again.
//
// A row's candidates are the tests whose coverage segment holds its lines
// (and the tests it names): only they can kill it. Its key is the verdict
// key of GT2 (rowKey) over the row with its candidates as its killers, so
// it moves when the row, the toolchain or anything a candidate's suite
// reads moves.

// killsVersion names kills.json's format and the meaning of its keys.
const killsVersion = "kills-v1"

// killRow is one row of the matrix.
type killRow struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Status string `json:"status"`
	// Candidates are the tests that could kill it; Killers those that did.
	Candidates []string `json:"candidates"`
	Killers    []string `json:"killers,omitempty"`
}

// killFile is kills.json.
type killFile struct {
	About   string    `json:"about"`
	Version string    `json:"version"`
	Rows    []killRow `json:"rows"`
}

// killsPath is the matrix file beside the ledger at ledger.
func killsPath(ledger string) string { return filepath.Join(filepath.Dir(ledger), "kills.json") }

// loadKills reads the matrix beside ledger by row name; a missing file, or
// one of another version, is an empty matrix.
func loadKills(ledger string) (map[string]killRow, error) {
	data, err := os.ReadFile(killsPath(ledger))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]killRow{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f killFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	out := map[string]killRow{}
	if f.Version != killsVersion {
		return out, nil
	}
	for _, r := range f.Rows {
		out[r.Name] = r
	}
	return out, nil
}

// saveKills writes rows (in the ledger's order) beside ledger.
func saveKills(ledger string, l *Ledger, rows map[string]killRow) error {
	f := killFile{
		About:   "Derived by `go run ./scripts/mutants -alpha` (CS-ALPHA): every killer of each row this package's alpha judged, by the row's key. Do not edit; a merge conflict takes either side and the next -alpha rewrites it.",
		Version: killsVersion,
	}
	for _, m := range l.Mutants {
		if r, ok := rows[m.Name]; ok {
			f.Rows = append(f.Rows, r)
		}
	}
	if len(f.Rows) == 0 {
		return nil
	}
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(f); err != nil {
		return err
	}
	return os.WriteFile(killsPath(ledger), b.Bytes(), 0o644)
}

// matrixRow is the row of r, judged with every candidate run.
func matrixRow(r Result, key string, candidates []string) killRow {
	row := killRow{Name: r.Name, Key: key, Status: r.Status, Candidates: candidates}
	if r.Status == Killed {
		row.Killers = slices.Clone(r.Killers)
		slices.Sort(row.Killers)
	}
	return row
}

// candidatesOf are the tests that can kill m: those whose segment in cov
// holds its lines, and those it names that cov knows. ok is false for a
// row whose edit does not apply (stale).
func candidatesOf(root string, cov *coverage, m *Mutant) ([]string, bool) {
	var r Result
	t, ok := (&worker{root: root}).locate(m, &r)
	if !ok {
		return nil, false
	}
	named := killerIDs(m)
	covering := cov.Covering(t.file, t.lo, t.hi, t.header, named)
	for _, id := range named {
		if cov.Lookup(id) != nil {
			covering[id] = true
		}
	}
	out := make([]string, 0, len(covering))
	for id := range covering {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, true
}

// matrixKey is the key of m's matrix row: GT2's row key with its
// candidates as its killers.
func matrixKey(in *reuseInputs, m *Mutant, candidates []string) string {
	if in == nil {
		return ""
	}
	return rowKey(in, &Mutant{Name: m.Name, File: m.File, Decl: m.Decl, From: m.From, To: m.To, Killers: candidates})
}
