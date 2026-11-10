// Package sizes measures the executables Ergo builds and holds them to a
// checked-in budget (binsize plan, BS0; NORTH_STAR "Small application
// binaries"). A receipt records, for one fixture, the file sizes of the
// default and the stripped build, the size of each ELF section, the text
// bytes of each Go package, the count of Go packages linked and of `main`
// text symbols, and the same numbers for a Go hello control built in the
// same run with the same toolchain. Ergo-only bytes are the fixture's size
// minus the control's: the subtraction cancels the growth of Go's own
// runtime between Go releases.
//
// The budget (budgets.json) holds, per fixture, a ceiling on Ergo-only
// bytes (default and stripped) and on the count of Go packages, and facts:
// patterns of symbol names that must not be linked. Facts are the main
// oracle, because they do not move with the Go release and they name the
// regression; a ceiling catches what no fact names. A ceiling is the
// measured value plus 2 %; a unit that shrinks a fixture lowers its
// ceiling. Receipts are printed, never checked in, so no generated number
// is pinned in the repository besides the reasoned ceilings.
package sizes

import (
	"cmp"
	"debug/elf"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Binary is what one build pair of a program measures: the default build
// (with its symbol table) and the stripped build.
type Binary struct {
	// Size is the default build's file size; Stripped the stripped one's.
	Size     int64 `json:"size"`
	Stripped int64 `json:"stripped"`
	// Sections is the size of each ELF section of the default build.
	Sections map[string]uint64 `json:"sections"`
	// Text is the text bytes of each Go package (symbols of executable
	// sections), read from the default build's symbol table.
	Text map[string]uint64 `json:"text"`
	// MainSymbols counts the text symbols of package main.
	MainSymbols int `json:"mainSymbols"`
	// GoPackages counts the Go packages the program links (go list -deps).
	GoPackages int `json:"goPackages"`
	// symbols are the default build's symbol names, for the facts.
	symbols []string
}

// Receipt is the measurement of one fixture against the Go hello control.
type Receipt struct {
	Fixture string `json:"fixture"`
	Binary
	Control Binary `json:"control"`
}

// ErgoOnly is the fixture's default file size minus the control's.
func (r Receipt) ErgoOnly() int64 { return r.Size - r.Control.Size }

// ErgoOnlyStripped is the fixture's stripped size minus the control's.
func (r Receipt) ErgoOnlyStripped() int64 { return r.Stripped - r.Control.Stripped }

// MarshalJSON adds the Ergo-only bytes to the receipt's record.
func (r Receipt) MarshalJSON() ([]byte, error) {
	type plain Receipt
	return json.Marshal(struct {
		plain
		ErgoOnly         int64 `json:"ergoOnly"`
		ErgoOnlyStripped int64 `json:"ergoOnlyStripped"`
	}{plain(r), r.ErgoOnly(), r.ErgoOnlyStripped()})
}

// MeasureBinary measures a build pair: sizes of both files, and sections,
// package text and symbols of the default build, which keeps its symbol
// table. GoPackages is the caller's (it needs the go command).
func MeasureBinary(defaultPath, strippedPath string) (Binary, error) {
	size, stripped, err := fileSizes(defaultPath, strippedPath)
	if err != nil {
		return Binary{}, err
	}
	file, err := elf.Open(defaultPath)
	if err != nil {
		return Binary{}, err
	}
	defer file.Close()
	b := Binary{Size: size, Stripped: stripped, Sections: map[string]uint64{}, Text: map[string]uint64{}}
	for _, s := range file.Sections {
		if s.Name != "" {
			b.Sections[s.Name] += s.Size
		}
	}
	symbols, err := file.Symbols()
	if err != nil {
		return Binary{}, fmt.Errorf("%s: %w (a stripped file has no symbols to measure)", defaultPath, err)
	}
	for _, s := range symbols {
		b.symbols = append(b.symbols, s.Name)
		if int(s.Section) >= len(file.Sections) || s.Section < elf.SectionIndex(1) {
			continue
		}
		if file.Sections[s.Section].Flags&elf.SHF_EXECINSTR == 0 {
			continue
		}
		pkg := Package(s.Name)
		b.Text[pkg] += s.Size
		if pkg == "main" {
			b.MainSymbols++
		}
	}
	return b, nil
}

// fileSizes are the sizes of the default and the stripped file.
func fileSizes(defaultPath, strippedPath string) (int64, int64, error) {
	full, err := os.Stat(defaultPath)
	if err != nil {
		return 0, 0, err
	}
	stripped, err := os.Stat(strippedPath)
	if err != nil {
		return 0, 0, err
	}
	return full.Size(), stripped.Size(), nil
}

// Package is the Go package a linker symbol belongs to: the import path
// before the first dot after the path's last slash, outside any type
// argument list or receiver. A linker-made symbol (type:, go:) is its
// prefix up to the first dot; an assembly symbol with no dot is "(asm)".
func Package(symbol string) string {
	if prefix, _, ok := strings.Cut(symbol, ":"); ok && (prefix == "type" || prefix == "go") {
		kind, _, _ := strings.Cut(symbol, ".")
		return kind
	}
	head := symbol
	if i := strings.IndexAny(head, "[("); i >= 0 {
		head = head[:i]
	}
	start := strings.LastIndexByte(head, '/') + 1
	if i := strings.IndexByte(head[start:], '.'); i >= 0 {
		return head[:start+i]
	}
	return "(asm)"
}

// Fixture is the budget of one fixture.
type Fixture struct {
	// ErgoOnly and ErgoOnlyStripped are ceilings on Receipt.ErgoOnly and
	// Receipt.ErgoOnlyStripped.
	ErgoOnly         int64 `json:"ergoOnly"`
	ErgoOnlyStripped int64 `json:"ergoOnlyStripped"`
	// GoPackages is a ceiling on the count of linked Go packages.
	GoPackages int `json:"goPackages"`
	// Forbid are facts: regular expressions no linked symbol may match.
	Forbid []string `json:"forbid"`
}

// Budget is budgets.json: a reason and the budget of each fixture.
type Budget struct {
	Reason   string             `json:"reason"`
	Fixtures map[string]Fixture `json:"fixtures"`
}

//go:embed budgets.json
var budgetFile []byte

// Checked is the repository's budget, budgets.json.
func Checked() (Budget, error) {
	var b Budget
	if err := json.Unmarshal(budgetFile, &b); err != nil {
		return Budget{}, fmt.Errorf("budgets.json: %w", err)
	}
	return b, nil
}

// Check returns each way the receipts leave the budget: a ceiling passed,
// a forbidden symbol linked, a fixture with no budget, or a budgeted
// fixture with no receipt. Each problem names the fixture.
func (b Budget) Check(receipts []Receipt) []string {
	var problems []string
	seen := map[string]bool{}
	for _, r := range receipts {
		seen[r.Fixture] = true
		f, ok := b.Fixtures[r.Fixture]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no budget in budgets.json", r.Fixture))
			continue
		}
		if r.ErgoOnly() > f.ErgoOnly {
			problems = append(problems, fmt.Sprintf("%s: Ergo-only bytes %d (file %d, Go hello %d) over the ceiling %d; largest package text differences: %s", r.Fixture, r.ErgoOnly(), r.Size, r.Control.Size, f.ErgoOnly, r.differences(5)))
		}
		if r.ErgoOnlyStripped() > f.ErgoOnlyStripped {
			problems = append(problems, fmt.Sprintf("%s: stripped Ergo-only bytes %d (file %d, Go hello %d) over the ceiling %d; largest package text differences: %s", r.Fixture, r.ErgoOnlyStripped(), r.Stripped, r.Control.Stripped, f.ErgoOnlyStripped, r.differences(5)))
		}
		if r.GoPackages > f.GoPackages {
			problems = append(problems, fmt.Sprintf("%s: %d Go packages linked, over the ceiling %d", r.Fixture, r.GoPackages, f.GoPackages))
		}
		for _, pattern := range f.Forbid {
			re, err := regexp.Compile(pattern)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: fact %q: %v", r.Fixture, pattern, err))
				continue
			}
			var matched []string
			for _, s := range r.symbols {
				if re.MatchString(s) {
					matched = append(matched, s)
				}
			}
			if len(matched) > 0 {
				problems = append(problems, fmt.Sprintf("%s: fact %q: %d linked symbols match, first %s", r.Fixture, pattern, len(matched), matched[0]))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(b.Fixtures)) {
		if !seen[name] {
			problems = append(problems, fmt.Sprintf("%s: budgeted in budgets.json, but no receipt", name))
		}
	}
	return problems
}

// differences lists the n packages whose text grew most over the control.
func (r Receipt) differences(n int) string {
	type diff struct {
		pkg   string
		bytes int64
	}
	var diffs []diff
	for pkg, size := range r.Text {
		if d := int64(size) - int64(r.Control.Text[pkg]); d > 0 {
			diffs = append(diffs, diff{pkg, d})
		}
	}
	slices.SortFunc(diffs, func(a, b diff) int { return cmp.Or(cmp.Compare(b.bytes, a.bytes), strings.Compare(a.pkg, b.pkg)) })
	var parts []string
	for _, d := range diffs[:min(n, len(diffs))] {
		parts = append(parts, fmt.Sprintf("%s +%d", d.pkg, d.bytes))
	}
	return strings.Join(parts, ", ")
}
