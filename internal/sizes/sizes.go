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

// Binary is what one build pair of a program measures: the build with its
// symbol table (`ergo build --debug`, or a Go build with default flags)
// and the stripped build (`ergo build`, or a Go build with -ldflags=-s).
type Binary struct {
	// Size is the unstripped build's file size; Stripped the stripped one's.
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
	// Packages, when set, are the only Go packages whose code the fixture
	// may link (Receipt.Text's packages, without the linker's own type:,
	// go: and assembly entries): a package outside the list fails the
	// budget by name, so a runtime import that pulls a package into every
	// program (runtime/debug's fmt and reflect, runtime/metrics; SIZE-HELLO)
	// is a red receipt, not a slow creep of the ceiling.
	Packages []string `json:"packages,omitempty"`
	// Base is the ceilings the fixture had before its first recorded raise,
	// and Raises are the raises since, each naming its bytes: no ceiling may
	// be above Base plus the raises (Checked), so a ceiling goes up only
	// with a raise that says which symbols the bytes are (SIZE-HELLO).
	// Lowering a ceiling needs no entry.
	Base   Ceilings `json:"base"`
	Raises []Raise  `json:"raises,omitempty"`
}

// Ceilings are the three ceilings of a fixture, or the amounts a raise
// adds to them.
type Ceilings struct {
	ErgoOnly         int64 `json:"ergoOnly"`
	ErgoOnlyStripped int64 `json:"ergoOnlyStripped"`
	GoPackages       int   `json:"goPackages"`
}

// Raise is one raise of a fixture's ceilings: the unit that made it, the
// amounts it adds, and what they are, by the guarantee that owns them.
type Raise struct {
	Unit string `json:"unit"`
	Ceilings
	// From and To name the two measured builds whose difference the raise
	// is (commits).
	From string `json:"from"`
	To   string `json:"to"`
	// Owners attribute the symbol bytes (go tool nm -size) between From
	// and To; the file bytes are those code bytes and their DWARF, line
	// tables and section rounding.
	Owners []Owner `json:"owners"`
	// Packages are the Go packages (go list -deps) To has and From has
	// not, at least as many as the raise's GoPackages, each with its owner.
	Packages []string `json:"packages,omitempty"`
}

// Owner is the share of a raise one guarantee or feature owns: its symbol
// bytes and its largest symbols, each with its byte change.
type Owner struct {
	Owner   string   `json:"owner"`
	Bytes   int64    `json:"bytes"`
	Symbols []string `json:"symbols"`
}

// ledger reports the first way f's ceilings are not accounted for: a
// ceiling above Base plus the raises, or a raise that names no bytes.
func (f Fixture) ledger() error {
	top := f.Base
	for i, r := range f.Raises {
		if r.Unit == "" || r.From == "" || r.To == "" || len(r.Owners) == 0 {
			return fmt.Errorf("raise %d names no unit, builds or owners", i+1)
		}
		if len(r.Packages) < r.GoPackages {
			return fmt.Errorf("raise %d (%s) adds %d Go packages but names %d", i+1, r.Unit, r.GoPackages, len(r.Packages))
		}
		for _, o := range r.Owners {
			if o.Owner == "" || o.Bytes == 0 || len(o.Symbols) == 0 {
				return fmt.Errorf("raise %d (%s): an owner names no bytes or symbols", i+1, r.Unit)
			}
		}
		top.ErgoOnly += r.ErgoOnly
		top.ErgoOnlyStripped += r.ErgoOnlyStripped
		top.GoPackages += r.GoPackages
	}
	switch {
	case f.ErgoOnly > top.ErgoOnly:
		return fmt.Errorf("ceiling ergoOnly %d is above base and raises, %d: a raise must name its bytes", f.ErgoOnly, top.ErgoOnly)
	case f.ErgoOnlyStripped > top.ErgoOnlyStripped:
		return fmt.Errorf("ceiling ergoOnlyStripped %d is above base and raises, %d: a raise must name its bytes", f.ErgoOnlyStripped, top.ErgoOnlyStripped)
	case f.GoPackages > top.GoPackages:
		return fmt.Errorf("ceiling goPackages %d is above base and raises, %d: a raise must name its packages", f.GoPackages, top.GoPackages)
	}
	return nil
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
	return b, b.ledgers()
}

// ledgers checks each fixture's ledger (Fixture.ledger).
func (b Budget) ledgers() error {
	for _, name := range slices.Sorted(maps.Keys(b.Fixtures)) {
		if err := b.Fixtures[name].ledger(); err != nil {
			return fmt.Errorf("budgets.json: %s: %w", name, err)
		}
	}
	return nil
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
		if len(f.Packages) > 0 {
			for _, pkg := range slices.Sorted(maps.Keys(r.Text)) {
				if linkedPackage(pkg) && !slices.Contains(f.Packages, pkg) {
					problems = append(problems, fmt.Sprintf("%s: links package %s (%d text bytes), which its budget's packages do not name", r.Fixture, pkg, r.Text[pkg]))
				}
			}
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

// linkedPackage reports whether pkg, a key of Receipt.Text, is a Go
// package and not one of the linker's own entries: type: and go: symbols,
// assembly with no package ("(asm)") and the blank symbol.
func linkedPackage(pkg string) bool {
	return pkg != "(asm)" && pkg != "_" && !strings.HasPrefix(pkg, "type:") && !strings.HasPrefix(pkg, "go:")
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
