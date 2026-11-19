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
	About string `json:"about"`
	// Env is NAME=VALUE entries that the runner gives every run of the
	// ledger's own suite, coverage pass and mutant pass: the switches its
	// killers need (ERGO_TS_TEST=1 for tsproj's TS leg), so that a plain
	// `go run ./scripts/mutants DIR` judges them. Only a package ledger
	// has one; check and lower share the ergo suites.
	Env []string `json:"env,omitempty"`
	// Commands are killers that are not a go test: a row names one as
	// command:NAME, and the runner runs its words in the mutated copy (the
	// words {root} and {bin:TOOL} as a suite's; a ledger's tool is one of
	// its scope). A command that fails kills, as a test does: the closure
	// sweep of cmd/ergo (rm-cgo-link-input) is one. A command runs only for
	// the rows that name it, in phase A, never in the coverage pass.
	Commands map[string][]string `json:"commands,omitempty"`
	Mutants  []*Mutant           `json:"mutants"`
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
	// Refused, when set, says why the formatter's or the compiler's
	// refusal of the mutated source is the rule the mutant breaks (a
	// diagnostic mutant). Without it, a source the formatter would rewrite
	// or the compiler refuses is invalid, never killed (Config.Sources).
	Refused string `json:"refused,omitempty"`
	// Regenerate, for a mutant of a generator, is the step that writes the
	// generator's output again (go generate): the runner runs it on the
	// mutated copy before the killers, and restores the files it wrote
	// after, so the killers judge what the mutated generator writes
	// (MUT-REGEN: H3f-consts-dropped survived, its killers reading the
	// unmutated output).
	Regenerate *Regenerate `json:"regenerate,omitempty"`
}

// Regenerate is a command run in Dir (relative to the module root, the
// directory of a nested module included); a failure makes the row
// invalid, as a mutated tree that does not build is.
type Regenerate struct {
	Dir  string   `json:"dir"`
	Argv []string `json:"argv"`
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
	for _, kv := range l.Env {
		if name, _, ok := strings.Cut(kv, "="); !ok || name == "" {
			return nil, fmt.Errorf("%s: env entry %q is not NAME=VALUE", path, kv)
		}
	}
	for name, words := range l.Commands {
		if name == "" || strings.ContainsAny(name, ": ") || len(words) == 0 {
			return nil, fmt.Errorf("%s: command %q needs a name without a colon or a space, and words", path, name)
		}
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
