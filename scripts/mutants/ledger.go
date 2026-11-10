package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Ledger is a package's mutant ledger, testdata/mutants.json: the mutants
// that the package's tests, the laws and the examples must kill. A test
// may be deleted only when every mutant of the ledger is still killed
// without it (docs/tooling.md, "Mutant ledger").
type Ledger struct {
	// About says what the file is, for a reader who opens it cold.
	About   string    `json:"about"`
	Mutants []*Mutant `json:"mutants"`
}

// Mutant is one edit of the package's source that some test must notice.
type Mutant struct {
	Name string `json:"name"`
	// Origin is the owner to tell when the mutant goes stale: the unit or
	// file that wrote it.
	Origin string `json:"origin"`
	// File is the mutated file, relative to the module root. From must
	// occur exactly once in it; the mutant replaces it with To.
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
	// Rule is the rule of the language or the tool that the edit breaks.
	Rule string `json:"rule"`
	// Killers are the tests known to kill the mutant, as SUITE:NAME; the
	// runner tries them first. An empty list with no Gap is a mutant the
	// runner has not judged yet.
	Killers []string `json:"killers"`
	// Gap, when set, says why no test kills the mutant: a known survivor,
	// a gap of the suite that a new test may name. It is not a failure.
	Gap string `json:"gap,omitempty"`
	// Anchor is set for an edit that has no coverage block of its own (a
	// string literal: an emitted Go template, a rule-text constant).
	Anchor *Anchor `json:"anchor,omitempty"`
}

// Anchor names the code that uses a mutated literal: the lines of Text (a
// string that occurs once in File) stand for the mutant's lines, and Tests
// (SUITE:NAME) cover it too, such as the tests that parse the source file.
type Anchor struct {
	File  string   `json:"file,omitempty"`
	Text  string   `json:"text,omitempty"`
	Tests []string `json:"tests,omitempty"`
}

func readLedger(path string) (*Ledger, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Ledger
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i, m := range l.Mutants {
		switch {
		case m.Name == "":
			return nil, fmt.Errorf("%s: mutant %d has no name", path, i)
		case seen[m.Name]:
			return nil, fmt.Errorf("%s: two mutants are named %q", path, m.Name)
		case m.File == "" || m.From == "":
			return nil, fmt.Errorf("%s: mutant %q needs a file and a from", path, m.Name)
		case m.From == m.To:
			return nil, fmt.Errorf("%s: mutant %q changes nothing", path, m.Name)
		}
		for _, k := range append(slices.Clone(m.Killers), anchorTests(m)...) {
			if !strings.Contains(k, ":") {
				return nil, fmt.Errorf("%s: mutant %q: test %q is not SUITE:NAME", path, m.Name, k)
			}
		}
		seen[m.Name] = true
	}
	return &l, nil
}

func anchorTests(m *Mutant) []string {
	if m.Anchor == nil {
		return nil
	}
	return m.Anchor.Tests
}

func writeLedger(path string, l *Ledger) error {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(l); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
