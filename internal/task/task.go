// Package task runs the generators of a workspace in the order their
// inputs and outputs give, and answers whether the generated files are
// what a fresh regeneration would write (docs/spec/build.md, Generators;
// MONOREPO WT1a).
//
// A generator (a Node) reads files and writes files of the tree. Its key
// is the content of what it reads: the files and folder listings of its
// manifest, and its environment. The manifest is the one the last run
// found (two steps, as Go's test cache keys a test on the files its last
// run opened): an input that moves the closure, an import line or a new
// file in a folder of it, is itself in the old manifest, so a stale
// manifest is a miss, never a stale hit. Outputs are not declared: a run
// observes them, the files whose stat a run changed. Edges follow: A runs
// before B when a file A writes is a file or in a folder B reads.
//
// A result is kept only for a run that held, and a hit needs the tree's
// outputs to hash as the run wrote them. A generator whose reads are
// declared by hand (ergo.work's reads lines) is Vouched: its key is only
// as good as the declaration, which Fresh mode checks (a hit that a
// fresh run contradicts is an error naming the generator).
package task

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// Class says what a node's key rests on, printed with every hit.
type Class string

const (
	// Derived: the toolchain computes the key; nothing is declared.
	Derived Class = "derived"
	// Vouched: the key holds the paths ergo.work's reads lines declare,
	// a promise the tool does not check except in Fresh mode.
	Vouched Class = "vouched: declared reads"
)

// Inputs is what a node reads: Files by content (a missing one counts as
// missing), Dirs by the names they hold, slash-separated paths relative
// to the root.
type Inputs struct {
	Files []string
	Dirs  []string
}

// Node is one generator of the workspace.
type Node struct {
	// Name is the node's name: the ergo.work gen's, or MEMBER:gen.
	Name  string
	Class Class
	// Ident is what names the node in the store besides its inputs: its
	// command and declared reads, so an edit of the declaration is a miss.
	Ident string
	// Env is the part of the key that is no file (the Go toolchain's
	// environment, the compiler's identity); computed once per run.
	Env func(ctx context.Context) (string, error)
	// Inputs are the node's inputs in the tree at root now (after a run:
	// the manifest the next lookup uses).
	Inputs func(ctx context.Context, root string) (Inputs, error)
	// Run writes the node's outputs in the tree at root; its output is
	// printed when it fails.
	Run func(ctx context.Context, root string) ([]byte, error)
}

// Mode is how Gen runs.
type Mode struct {
	// Check writes nothing in the tree: a node that misses runs in a copy
	// of the tree, and Gen fails when the copy's outputs differ from the
	// tree's.
	Check bool
	// Fresh ignores hits: every node runs; a stored result that the fresh
	// run contradicts is an error naming the node.
	Fresh bool
}

// Runner is what Gen needs besides the nodes.
type Runner struct {
	// Root is the workspace root, absolute.
	Root string
	// Store keeps the results; nil keeps none (every node runs).
	Store *lawcache.Store
	// Out receives one line per node and pass.
	Out io.Writer
	// Scratch makes an empty folder for the copy Check runs in, and
	// removes it; nil uses scratchDir.
	Scratch func() (dir string, remove func(), err error)
	// Ran, when set, is told each node that ran and the root it ran in
	// (the property's witness of order and counts).
	Ran func(name, root string)
	// Logs is the folder a step's output is kept in (Verdicts); "" keeps
	// none.
	Logs string
	// Flaky is the file each flaky step is appended to; "" notes none.
	Flaky string
	// RunWrites, when set, names the key parts a run of the steps writes
	// itself (the law ledger), which Verdicts settles at the run's end.
	RunWrites func(part string) bool
}

// Report is what Gen did.
type Report struct {
	// Order is the order of the last pass, by node name.
	Order []string
	// Ran counts the runs of each node, Hits its hits.
	Ran, Hits map[string]int
	// Stale are the files a fresh regeneration would change, sorted
	// (Check only).
	Stale []string
	// Contradicted are the nodes whose stored result a fresh run
	// contradicted (Fresh only): their declared reads are incomplete.
	Contradicted []string
	// Passes is the number of passes until nothing ran.
	Passes int
}

// Failed reports whether the report fails the run: a stale file or a
// contradicted hit.
func (r Report) Failed() bool { return len(r.Stale) > 0 || len(r.Contradicted) > 0 }

// storeIdentity is the store's compiler identity for task results: they
// do not depend on the ergo binary's identity (a Derived node puts it in
// its Env), so a rebuilt ergo keeps them.
const StoreIdentity = "ergo-task/1"

// record is what the store keeps of a node's last run: the environment,
// the manifest and the outputs, each output with its digest ("-" for a
// file the run removed).
type record struct {
	env string
	in  Inputs
	// fileSums and dirSums are the digest of each of in.Files and the
	// listing digest of each of in.Dirs when the run's key was made, so
	// that a later miss can name the input that differs (a miss always
	// has a reason: lookup's why).
	fileSums, dirSums []string
	outs              []output
}

type output struct {
	path, digest string
}

func (r record) encode() string {
	var b strings.Builder
	b.WriteString("ergo-task/2\n")
	fmt.Fprintf(&b, "env %q\n", r.env)
	for i, f := range r.in.Files {
		fmt.Fprintf(&b, "in %s %s\n", r.fileSums[i], f)
	}
	for i, d := range r.in.Dirs {
		fmt.Fprintf(&b, "dir %s %s\n", r.dirSums[i], d)
	}
	for _, o := range r.outs {
		fmt.Fprintf(&b, "out %s %s\n", o.digest, o.path)
	}
	return b.String()
}

func decodeRecord(text string) (record, bool) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 2 || lines[0] != "ergo-task/2" {
		return record{}, false
	}
	var r record
	for _, l := range lines[1:] {
		key, rest, _ := strings.Cut(l, " ")
		switch key {
		case "env":
			if _, err := fmt.Sscanf(rest, "%q", &r.env); err != nil {
				return record{}, false
			}
		case "in", "dir":
			sum, p, ok := strings.Cut(rest, " ")
			if !ok {
				return record{}, false
			}
			if key == "in" {
				r.in.Files, r.fileSums = append(r.in.Files, p), append(r.fileSums, sum)
			} else {
				r.in.Dirs, r.dirSums = append(r.in.Dirs, p), append(r.dirSums, sum)
			}
		case "out":
			d, p, ok := strings.Cut(rest, " ")
			if !ok {
				return record{}, false
			}
			r.outs = append(r.outs, output{path: p, digest: d})
		default:
			return record{}, false
		}
	}
	return r, true
}

// lastKey is the store key of a node's last run in the tree at r.Root
// (never the --check copy's). The store is shared by every checkout of
// the host (one cache root), and the last run is a fact about one tree:
// keyed without the root, a gen in another checkout overwrites it, and
// this tree's next lookup reads that checkout's manifest and misses with
// nothing changed here (TestGenTreesShareAStore; the likeliest cause of
// the MONOREPO WT1b c10 miss). Entries stay keyed by content alone, so
// trees still share results.
func (r *Runner) lastKey(n Node) string {
	return lawcache.Key("task last", r.Root, n.Name, n.Ident)
}

// sortedUnique sorts s and drops repeats.
func sortedUnique(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}
