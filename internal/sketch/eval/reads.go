package eval

import (
	"sort"

	"ergo.local/prototype/internal/sketch/check"
)

// reads.go keeps the packages each obligation's evaluation entered
// (GB-WS0C): a verdict is valid for what it read, so a cache may reuse
// it after an edit to any package outside them (Bazel's discovered
// inputs; Salsa's recorded dependencies). Every way evaluation observes
// another package's bodies enters it here:
//
//   - a call of a declared function, whatever dispatched it (an operation,
//     a witness, a provider's New): callPlanned;
//   - a function literal's body: callClosure, its home's package;
//   - a law's body, its own package's or an operation type's inherited
//     one: runLawTarget;
//   - a body preflight walks before the cases run: preflight.function;
//   - package lets and machine initials, which every obligation reads:
//     the world initialize records;
//   - a licence run's Go bodies: the module's own packages, entered by
//     the law's package and the calls that reach them.
//
// What check folded (constants, types, method sets) is in the packages'
// interfaces, which the cache keys separately.

// readSet is the packages entered by the obligation in progress. epoch
// counts obligations, so a plan records its package once per obligation
// (funcPlan.readAt) at the cost of one compare per call.
type readSet struct {
	epoch uint64
	pkgs  map[*check.Package]bool
	// world is what the initializers entered: part of every obligation.
	world map[*check.Package]bool
}

// readsBegin starts the reads of a new obligation: the world's.
func (in *interp) readsBegin() {
	in.reads.epoch++
	in.reads.pkgs = make(map[*check.Package]bool, len(in.reads.world))
	for p := range in.reads.world {
		in.reads.pkgs[p] = true
	}
}

// readsWorld makes the reads so far the world every obligation reads.
func (in *interp) readsWorld() {
	in.reads.world = in.reads.pkgs
}

// enter records that the obligation in progress entered p.
func (in *interp) enter(p *check.Package) {
	if p == nil || p.Host() || in.reads.pkgs == nil {
		return
	}
	in.reads.pkgs[p] = true
}

// enterPlan is enter of p's package, once per obligation.
func (in *interp) enterPlan(p *funcPlan) {
	if p.readAt != in.reads.epoch {
		p.readAt = in.reads.epoch
		in.enter(p.fn.Pkg)
	}
}

// readsSoFar is the union of now and the reads of every result rep holds:
// what a result the run's earlier obligations decided (not run after a
// defect) read.
func readsSoFar(rep *Report, now []string) []string {
	sets := [][]string{now}
	for _, r := range rep.Results {
		sets = append(sets, r.Reads)
	}
	return unionPaths(sets...)
}

// unknownSoFar says some result rep holds has reads its store did not keep.
func unknownSoFar(rep *Report) bool {
	for _, r := range rep.Results {
		if r.ReadsUnknown {
			return true
		}
	}
	return false
}

// unionPaths is the sorted union of sorted path lists.
func unionPaths(sets ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sets {
		for _, p := range s {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// readPaths are the paths of the packages the obligation in progress
// entered, sorted.
func (in *interp) readPaths() []string {
	out := make([]string, 0, len(in.reads.pkgs))
	for p := range in.reads.pkgs {
		out = append(out, p.Path)
	}
	sort.Strings(out)
	return out
}
