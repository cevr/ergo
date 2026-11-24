// Command nou checks the shrinking inventory of calls to the legacy
// Unreachable prelude binding. Run from the repository root:
//
//	go run ./scripts/nou check
//	go run ./scripts/nou check --base BASE
//	go run ./scripts/nou prune         # preview the replacement on stdout
//	go run ./scripts/nou prune -write  # atomically replace the ledger
//
// For the one landing that activates Go fixture coverage, regenerate from
// the accepted pre-merge main (not the candidate), then prune any sites
// retired by the merge and check it:
//
//	go run ./scripts/nou bootstrap-go --base BASE --write
//	go run ./scripts/nou prune -write
//	go run ./scripts/nou check
//
// Prune refuses to add a site, even when another site has disappeared.
package main

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const ledger = "scripts/nou/testdata/sites.tsv"
const goLedgerSeal = "# Go fixture and template scopes active"

type allowance struct {
	family, scope string
	count         int
}

func main() {
	const usage = "usage: nou check [--base REV] | prune [-write] | inventory | bootstrap-go --base REV --write"
	var baseRef string
	var pruneWrite bool
	switch {
	case len(os.Args) == 5 && os.Args[1] == "bootstrap-go" && os.Args[2] == "--base" && os.Args[3] != "" && os.Args[4] == "--write":
		baseRef = os.Args[3]
	case len(os.Args) == 4 && os.Args[1] == "check" && os.Args[2] == "--base" && os.Args[3] != "":
		baseRef = os.Args[3]
	case len(os.Args) == 3 && os.Args[1] == "prune" && (os.Args[2] == "-write" || os.Args[2] == "--write"):
		pruneWrite = true
	case len(os.Args) == 2 && os.Args[1] != "bootstrap-go":
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help":
		fmt.Println(usage)
		return
	case "check", "prune", "inventory", "bootstrap-go":
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], baseRef, pruneWrite, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nou:", err)
		os.Exit(1)
	}
}

func run(cmd, baseRef string, pruneWrite bool, out, diagnostics io.Writer) error {
	root, err := filepath.Abs(".")
	if err != nil {
		return err
	}
	if cmd == "bootstrap-go" {
		return bootstrapGo(root, baseRef, diagnostics)
	}
	current, err := inventory(root)
	if err != nil {
		return err
	}
	if cmd == "inventory" {
		for _, s := range current {
			fmt.Fprintf(out, "%s\t%d\t%s\t%s\n", s.id.file, s.line, sourceScope(s.id.file), s.id.anchor)
		}
		return nil
	}
	name := filepath.Join(root, ledger)
	base, err := os.Open(name)
	if err != nil {
		return err
	}
	rows, err := readLedger(base)
	base.Close()
	if err != nil {
		return err
	}
	if baseRef != "" {
		if err := checkParent(root, baseRef, rows, diagnostics); err != nil {
			return err
		}
	}
	limit := make(map[siteID]int, len(rows))
	for id, row := range rows {
		limit[id] = row.count
	}
	excess := newSites(current, limit)
	for _, s := range excess {
		fmt.Fprintf(diagnostics, "%s:%d: new legacy site %s\n", s.id.file, s.line, s.id.anchor)
	}
	if len(excess) > 0 {
		return fmt.Errorf("%d new legacy sites; replace the legacy call, not its baseline", len(excess))
	}
	have := map[siteID]int{}
	for _, s := range current {
		have[s.id]++
	}
	summary := map[string][2]int{}
	removed := 0
	for id, row := range rows {
		key := row.family + "\t" + row.scope
		n := summary[key]
		n[0] += row.count
		n[1] += have[id]
		summary[key] = n
		removed += row.count - have[id]
	}
	for _, key := range slices.Sorted(maps.Keys(summary)) {
		n := summary[key]
		fmt.Fprintf(diagnostics, "%s\t%d -> %d (%+d)\n", key, n[0], n[1], n[1]-n[0])
	}
	if cmd == "prune" {
		return pruneLedger(name, pruneWrite, out, rows, have)
	}
	if removed > 0 {
		return fmt.Errorf("%d retired legacy sites still in the baseline; run nou prune -write and commit the deletions", removed)
	}
	fmt.Fprintf(out, "%d legacy sites, no additions or stale rows\n", len(current))
	return nil
}

func checkParent(root, ref string, rows map[siteID]allowance, diagnostics io.Writer) error {
	raw, err := gitOutput(root, "show", "--no-ext-diff", "--end-of-options", ref+":"+ledger)
	if err != nil {
		return fmt.Errorf("read baseline at %s: %w", ref, err)
	}
	parent, err := readLedger(strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	var excess []string
	for id, row := range rows {
		prior := parent[id]
		if row.count > prior.count || row.family != prior.family || row.scope != prior.scope {
			excess = append(excess, id.file+": "+id.anchor)
		}
	}
	slices.Sort(excess)
	for _, line := range excess {
		fmt.Fprintln(diagnostics, "baseline row increased or changed owner:", line)
	}
	if len(excess) > 0 {
		return fmt.Errorf("%d baseline rows increased or changed owner relative to %s", len(excess), ref)
	}
	return nil
}

func gitOutput(root string, args ...string) ([]byte, error) {
	git := exec.Command("git", args...)
	git.Dir = root
	return git.Output()
}

func readLedger(r io.Reader) (map[siteID]allowance, error) {
	rows := map[siteID]allowance{}
	sc := bufio.NewScanner(r)
	for i := 1; sc.Scan(); i++ {
		if strings.HasPrefix(sc.Text(), "#") {
			continue
		}
		fields := strings.Split(sc.Text(), "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("%s:%d: expected family, scope, path, anchor, count", ledger, i)
		}
		family, scope, file, anchor := fields[0], fields[1], fields[2], fields[3]
		count, err := strconv.Atoi(fields[4])
		id := siteID{file, anchor}
		if err != nil || count < 1 || scope != sourceScope(file) || anchor == "" ||
			len(family) != 1 || !strings.Contains("ABCDEFGRTX", family) || rows[id].count != 0 {
			return nil, fmt.Errorf("%s:%d: invalid or duplicate baseline row", ledger, i)
		}
		rows[id] = allowance{family, scope, count}
	}
	return rows, sc.Err()
}

func writeLedger(w io.Writer, rows map[siteID]allowance, counts map[siteID]int) error {
	b := bufio.NewWriter(w)
	fmt.Fprintln(b, "# family\tsource scope\tpath\tdeclaration/local anchor\tcount")
	fmt.Fprintln(b, goLedgerSeal)
	for _, id := range sortedSiteIDs(counts) {
		r := rows[id]
		fmt.Fprintf(b, "%s\t%s\t%s\t%s\t%d\n", r.family, r.scope, id.file, id.anchor, counts[id])
	}
	return b.Flush()
}

func sortedSiteIDs[V any](rows map[siteID]V) []siteID {
	ids := slices.Collect(maps.Keys(rows))
	slices.SortFunc(ids, func(a, b siteID) int {
		if n := strings.Compare(a.file, b.file); n != 0 {
			return n
		}
		return strings.Compare(a.anchor, b.anchor)
	})
	return ids
}

func sourceScope(file string) string {
	switch {
	case path.Ext(file) == ".go":
		if strings.HasSuffix(file, "_test.go") ||
			strings.HasPrefix(file, "internal/experimental/irspike/gen_") {
			return "embedded Go tests/property source"
		}
		return "generator template"
	case strings.Contains(file, ".txtar::"):
		return "archive fixture"
	case strings.Contains(file, "/testdata/"):
		return "fixture"
	case strings.HasPrefix(file, stdDir+"/"):
		return "std"
	case strings.HasPrefix(file, "internal/linked/"):
		return "linked generated"
	default:
		return strings.SplitN(path.Clean(file), "/", 2)[0]
	}
}
