package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The package key: the fast tier's replay of one Go test package (or one
// shard of it), as Bazel keys a test action on its declared inputs. A
// package's key is the whole-gate facts (Facts) projected onto the
// package's closure: the files the go command compiles into its test
// binary, its own testdata, and the inputs internal/closure declares for it
// (Allowance), with every fact that is not a path: the gate executable, the
// checkout root, the environment a step reads, the gate's settings (the
// tier among them, WithTier), and the host fingerprint, which holds every
// declared host program (the tool fingerprints) and the CPU budget. HEAD
// and the index are in it only when the closure reaches .git. A package
// whose key is unchanged replays its last pass; any other runs, with
// -count=1, so no go test inside it replays Go's test cache.

// Grant is the kind of access a declared input covers, as internal/closure
// declares it.
type Grant string

const (
	// GrantFile covers exactly the path; when the path is a directory, a
	// read of it is a listing, so its entries are covered too.
	GrantFile Grant = "file"
	// GrantListing covers the directory (a path.Match pattern) and the
	// names in it.
	GrantListing Grant = "listing"
	// GrantFiles covers every path the pattern matches (path.Match).
	GrantFiles Grant = "files"
	// GrantSubtree covers the directory and every path below it; "." is
	// the whole checkout, .git included.
	GrantSubtree Grant = "subtree"
	// GrantProgram covers what the go command reads to build the module
	// package at the path: its Closure in Programs.
	GrantProgram Grant = "program"
)

// Allowance is one declared input of a test package outside its compiled
// closure: a path relative to the module root, with slashes, and its grant.
type Allowance struct {
	Path  string
	Grant Grant
}

// Closure is what one test package may read, the rule of internal/closure.
type Closure struct {
	// Dir is the package directory, relative to the module root.
	Dir string
	// Deps are the directories of the packages compiled into its test
	// binary that lie in the module, its own included.
	Deps []string
	// Embeds are the files those packages embed, relative to the root.
	Embeds []string
	// Declared are its inputs outside the compiled closure.
	Declared []Allowance
	// Programs are the compiled closures of the GrantProgram entries, by
	// path.
	Programs map[string]*Closure

	deps, ancestors, embeds map[string]bool
}

// moduleLookups are the names the go command looks up anywhere to find a
// module's root and layout (internal/closure moduleLookups).
var moduleLookups = map[string]bool{"go.mod": true, "go.work": true, "go.work.sum": true, "vendor": true}

// everyPackage are the files any test package reads: the module file and
// its sums (internal/closure everyPackage).
var everyPackage = []string{"go.mod", "go.sum"}

func (c *Closure) index() {
	if c.deps != nil {
		return
	}
	c.deps, c.ancestors, c.embeds = map[string]bool{c.Dir: true}, map[string]bool{}, map[string]bool{}
	for _, d := range c.Deps {
		c.deps[d] = true
	}
	for d := range c.deps {
		for a := path.Dir(d); ; a = path.Dir(a) {
			c.ancestors[a] = true
			if a == "." {
				break
			}
		}
	}
	for _, e := range c.Embeds {
		c.embeds[e] = true
		for a := path.Dir(e); !c.deps[a] && a != "."; a = path.Dir(a) {
			c.embeds[a] = true
		}
	}
}

// compiled reports whether p is read to build the test binary: a file of a
// dependency's directory, the directory itself, an embedded file or its
// directory, or a directory on the way to a dependency.
func (c *Closure) compiled(p string) bool {
	c.index()
	return c.deps[p] || c.deps[path.Dir(p)] && p != "." || c.embeds[p] || c.ancestors[p]
}

// Covers reports whether the checkout path p (relative, with slashes) is an
// input of the package.
func (c *Closure) Covers(p string) bool {
	switch {
	case c.compiled(p):
		return true
	case p == c.Dir+"/testdata" || strings.HasPrefix(p, c.Dir+"/testdata/"):
		return true
	case moduleLookups[path.Base(p)]:
		return true
	case slices.Contains(everyPackage, p):
		return true
	}
	for _, a := range c.Declared {
		if a.covers(p, c.Programs) {
			return true
		}
	}
	return false
}

func (a Allowance) covers(p string, programs map[string]*Closure) bool {
	match := func(pattern, name string) bool {
		ok, err := path.Match(pattern, name)
		return ok && err == nil
	}
	switch a.Grant {
	case GrantFile:
		return p == a.Path || path.Dir(p) == a.Path && p != "."
	case GrantListing:
		return match(a.Path, p) || p != "." && match(a.Path, path.Dir(p))
	case GrantFiles:
		return match(a.Path, p)
	case GrantSubtree:
		return a.Path == "." || p == a.Path || strings.HasPrefix(p, a.Path+"/")
	case GrantProgram:
		if program, ok := programs[a.Path]; ok {
			return program.compiled(p)
		}
		// A program with no closure cannot be keyed by its parts: every
		// path is its input.
		return true
	}
	// An unknown grant covers everything: the key reruns, never replays
	// stale.
	return true
}

// ReadsGit reports whether the package's declared inputs reach .git, so
// that HEAD and the index are inputs.
func (c *Closure) ReadsGit() bool {
	for _, a := range c.Declared {
		if a.Grant != GrantProgram && a.covers(".git", nil) {
			return true
		}
	}
	return false
}

// Select returns the entries of the facts the closure covers, in path
// order, with what the symlinks among them lead to in the checkout: a link
// keys its target text, and what it reaches is read through it.
func (c *Closure) Select(entries []Entry) []Entry {
	var selected []Entry
	taken := map[string]bool{}
	var reached []string
	for _, e := range entries {
		if c.Covers(e.Path) {
			selected = append(selected, e)
			taken[e.Path] = true
			if e.Kind == Symlink {
				reached = append(reached, reachedPath(e.Reach))
			}
		}
	}
	// What a link reaches, and what a link there reaches, to a fixed point.
	for len(reached) > 0 {
		place := reached[0]
		reached = reached[1:]
		if place == "" {
			continue
		}
		for _, e := range entries {
			if taken[e.Path] || !(e.Path == place || place == "." || strings.HasPrefix(e.Path, place+"/")) {
				continue
			}
			selected = append(selected, e)
			taken[e.Path] = true
			if e.Kind == Symlink {
				reached = append(reached, reachedPath(e.Reach))
			}
		}
	}
	slices.SortFunc(selected, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	return selected
}

// reachedPath is the checkout path a link's Reach names, or "" when it
// leads into a keyed root (the host fingerprint covers it).
func reachedPath(reach string) string {
	place, ok := strings.CutPrefix(reach, "checkout ")
	if !ok {
		return ""
	}
	place = strings.TrimPrefix(place, "missing ")
	if place == "" {
		return "."
	}
	return place
}

// PackageKey is the key of one package step: identity names the step (its
// name, argv, variables and selection), facts are the run's facts with its
// tier (WithTier). It returns the key, or a reason the package can neither
// replay nor be recorded: facts that cannot key a replay, or a stat-keyed
// input changed within RecentWindow.
func PackageKey(facts *Facts, c *Closure, identity ...string) (key, problem string) {
	parts, problem := packageKeyParts(facts, c, identity)
	if problem != "" {
		return "", problem
	}
	h := sha256.New()
	put(h, "package", Version)
	for _, input := range packageKeyInputs {
		for _, part := range parts[input] {
			put(h, append([]string{string(input)}, part...)...)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), ""
}

// keyInput is one input class of the package key.
type keyInput string

const (
	inputIdentity    keyInput = "identity"
	inputExecutable  keyInput = "executable"
	inputRoot        keyInput = "root"
	inputClosure     keyInput = "closure"
	inputDeclared    keyInput = "declared"
	inputHead        keyInput = "head"
	inputIndex       keyInput = "index"
	inputEntry       keyInput = "entry"
	inputEnvironment keyInput = "environment"
	inputSetting     keyInput = "setting"
	inputHost        keyInput = "host"
)

// packageKeyInputs are the input classes of the package key, in the order
// it hashes them: the one list of them. PackageKey hashes nothing else but
// its version, and the package key model has, for each class here, an
// action that changes that class alone (TestPackageKeyModelChangesEachInputAlone).
var packageKeyInputs = []keyInput{
	inputIdentity, inputExecutable, inputRoot, inputClosure, inputDeclared, inputHead, inputIndex,
	inputEntry, inputEnvironment, inputSetting, inputHost,
}

// packageKeyParts are the parts of a package's key by input class, or a
// reason it has no key.
func packageKeyParts(facts *Facts, c *Closure, identity []string) (map[keyInput][][]string, string) {
	if problem := facts.Problem(); problem != "" {
		return nil, problem
	}
	facts.sort()
	parts := map[keyInput][][]string{}
	add := func(input keyInput, part ...string) { parts[input] = append(parts[input], part) }
	for _, part := range identity {
		add(inputIdentity, part)
	}
	add(inputExecutable, facts.Executable)
	add(inputRoot, facts.Root)
	add(inputClosure, c.Dir)
	for _, a := range c.Declared {
		add(inputDeclared, a.Path, string(a.Grant))
	}
	if c.ReadsGit() {
		add(inputHead, facts.Head)
		for _, entry := range facts.Index {
			add(inputIndex, entry)
		}
	}
	recent := map[string]bool{}
	for _, p := range facts.Recent {
		recent[p] = true
	}
	for _, e := range c.Select(facts.Entries) {
		if recent[e.Path] {
			return nil, fmt.Sprintf("%s changed within %v of the key", e.Path, RecentWindow)
		}
		add(inputEntry, e.Path, string(e.Class), string(e.Kind), e.Value, e.Reach)
	}
	for _, v := range facts.Environment {
		add(inputEnvironment, v.Name, strconv.FormatBool(v.Set), v.Value)
	}
	for _, v := range facts.Settings {
		add(inputSetting, v.Name, strconv.FormatBool(v.Set), v.Value)
	}
	for _, fact := range facts.Host {
		add(inputHost, fact.Name, fact.Value)
	}
	return parts, ""
}

// TierSetting is the setting that names the run's tier in every key.
const TierSetting = "gate tier"

// WithTier returns facts with the run's tier among the settings, so a key
// made from them (Key, PackageKey) names the tier: a record of one tier
// never replays for another, and the merge tier, which keys the full tier,
// never finds a fast record.
func WithTier(facts *Facts, tier string) *Facts {
	facts.Settings = append(facts.Settings, Variable{Name: TierSetting, Set: tier != "", Value: tier})
	return facts
}

// Packages is the store of package records: the same objects as the
// whole-gate store, records in a directory of their own, so no package
// record is ever read as a whole-gate one.
func (s Store) Packages() Store {
	s.records = "packages"
	return s
}

// RecordPackage ends one package step of a run. A passed step whose key at
// the end of the run is its key at the start records its output under the
// key, by the store's protocol (the object first, then the record by one
// atomic rename); a failed step withdraws any record at its key. It
// returns a line for the log, or "".
func (s Store) RecordPackage(key, endKey string, passed bool, step Step, object *Object, now time.Time) string {
	if !passed {
		if existed, err := s.withdraw(key); err != nil {
			return fmt.Sprintf("gate: %s: withdraw package record %s: %v", step.Name, key[:12], err)
		} else if existed {
			return fmt.Sprintf("gate: %s: withdrew package record %s", step.Name, key[:12])
		}
		return ""
	}
	if endKey != key {
		return fmt.Sprintf("gate: %s: not recorded: its inputs changed during the run", step.Name)
	}
	if object == nil {
		return fmt.Sprintf("gate: %s: not recorded: no stored output", step.Name)
	}
	sum, size, err := object.commit()
	if err != nil {
		return fmt.Sprintf("gate: %s: not recorded: %v", step.Name, err)
	}
	step.Object, step.Bytes = sum, size
	if err := s.write(Record{Version: Version, Key: key, Recorded: now.UTC(), Steps: []Step{step}}); err != nil {
		return fmt.Sprintf("gate: %s: not recorded: %v", step.Name, err)
	}
	return ""
}
