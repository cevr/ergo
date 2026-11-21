package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/scripts/mutants/covfile"
)

// replayAffected replays the round tier over the first-parent tips of a
// range (CS-ROUND acceptance 3): for each tip, the change tip^1..tip judged
// against the coverage store's segments at its base, and the share of the
// recorded test seconds (durationsFile) the selection keeps. The base is
// tip^1 when the store's index names it, else the indexed commit nearest
// to the tip (a segment is the coverage of its commit's tree, so the diff
// from that commit to the tip is a sound change to judge, if a wider one);
// a package with no segment there runs every test, and the row says how
// many did. reds, when set, holds lines "TIP DIR:TEST", the tests a tip
// turned red: each must be in its tip's selection. It writes one row per
// tip to out, then the median share and the count, and returns the reds
// the selection missed.
func replayAffected(root, span, redsPath string, out io.Writer) ([]string, error) {
	from, to, ok := strings.Cut(span, "..")
	if !ok {
		return nil, fmt.Errorf("-replay-affected %q: want FROM..TO", span)
	}
	list, err := gitText(root, "rev-list", "--first-parent", "--reverse", from+".."+to)
	if err != nil {
		return nil, err
	}
	reds := map[string][]string{}
	if redsPath != "" {
		data, err := os.ReadFile(redsPath)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 2 {
				continue
			}
			tip, err := gitText(root, "rev-parse", "--verify", fields[0]+"^{commit}")
			if err != nil {
				return nil, err
			}
			reds[tip] = append(reds[tip], fields[1])
		}
	}
	store, err := covfile.Open()
	if err != nil {
		return nil, err
	}
	indexed, err := store.Commits()
	if err != nil {
		return nil, err
	}
	if len(indexed) == 0 {
		return nil, fmt.Errorf("no coverage store at %s: run go run ./scripts/mutants -measure", store.Dir)
	}
	packages, _, err := testPackages(root)
	if err != nil {
		return nil, err
	}
	durations, err := readDurations(root)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, "tip\tbase\tselected_s\ttotal_s\tshare\tfail_closed\treason")
	var shares []float64
	var missed []string
	for _, tip := range strings.Fields(list) {
		base, err := gitText(root, "rev-parse", tip+"^1")
		if err != nil {
			return nil, err
		}
		if !slices.Contains(indexed, base) {
			base = nearestIndexed(root, indexed, tip)
		}
		if base == "" {
			fmt.Fprintf(out, "%s\t-\t-\t-\t1\t-\tno indexed commit is related to the tip\n", short(tip))
			shares = append(shares, 1)
			continue
		}
		entries, err := store.At(base)
		if err != nil {
			return nil, err
		}
		changes, err := changesBetween(root, base, tip)
		if err != nil {
			return nil, err
		}
		var selected, total float64
		closed, reason := 0, ""
		chosen := map[string]roundPick{}
		for _, p := range packages {
			seconds := durations[p.importPath]
			sum := 0.0
			for _, s := range seconds {
				sum += s
			}
			total += sum
			entry, ok := entryOf(entries, p.dir)
			seg, loaded := (*covfile.Coverage)(nil), false
			if ok {
				seg, loaded = store.Load(entry.Key)
			}
			if !loaded {
				closed++
				selected += sum
				chosen[p.dir] = roundPick{all: "no segment at the base"}
				continue
			}
			names := slices.Sorted(func(yield func(string) bool) {
				for name := range seconds {
					if !yield(name) {
						return
					}
				}
			})
			pick := pickRound(entry, seg, p.closure, changes, names)
			chosen[p.dir] = pick
			if pick.all != "" {
				selected += sum
				if reason == "" {
					reason = p.dir + ": " + pick.all
				}
				continue
			}
			for name := range pick.run {
				selected += seconds[name]
			}
		}
		share := 1.0
		if total > 0 {
			share = selected / total
		}
		shares = append(shares, share)
		fmt.Fprintf(out, "%s\t%s\t%.0f\t%.0f\t%.3f\t%d\t%s\n", short(tip), short(base), selected, total, share, closed, reason)
		for _, red := range reds[tip] {
			dir, test, _ := strings.Cut(red, ":")
			pick, ok := chosen[dir]
			if !ok {
				missed = append(missed, short(tip)+" "+red+" (no such package)")
				continue
			}
			if _, run := pick.run[test]; pick.all == "" && !run {
				missed = append(missed, short(tip)+" "+red)
			}
		}
	}
	slices.Sort(shares)
	median := 1.0
	if len(shares) > 0 {
		median = shares[len(shares)/2]
	}
	fmt.Fprintf(out, "median share %.3f over %d tips; %d reds missed\n", median, len(shares), len(missed))
	return missed, nil
}

// runReplayAffected is -replay-affected: it returns the exit code.
func runReplayAffected(span, outPath, redsPath string) int {
	root, err := repositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate:", err)
		return 2
	}
	var out io.Writer = os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gate:", err)
			return 2
		}
		defer f.Close()
		out = f
	}
	missed, err := replayAffected(root, span, redsPath, out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate:", err)
		return 2
	}
	if len(missed) > 0 {
		fmt.Fprintf(os.Stderr, "gate: the selection misses %d reds:\n%s\n", len(missed), strings.Join(missed, "\n"))
		return 1
	}
	return 0
}

// nearestIndexed is the indexed commit with the fewest commits between it
// and tip, either way, or "" when none shares history with it.
func nearestIndexed(root string, indexed []string, tip string) string {
	best, distance := "", -1
	for _, c := range indexed {
		counts, err := gitText(root, "rev-list", "--count", "--left-right", c+"..."+tip)
		if err != nil {
			continue
		}
		fields := strings.Fields(counts)
		if len(fields) != 2 {
			continue
		}
		a, _ := strconv.Atoi(fields[0])
		b, _ := strconv.Atoi(fields[1])
		if distance < 0 || a+b < distance {
			best, distance = c, a+b
		}
	}
	return best
}

// changesBetween is the change from commit base to commit tip.
func changesBetween(root, base, tip string) ([]fileChange, error) {
	diff, err := gitText(root, "diff", "--name-only", "-z", "--no-renames", base, tip, "--")
	if err != nil {
		return nil, err
	}
	var out []fileChange
	for _, name := range strings.Split(diff, "\x00") {
		if name == "" {
			continue
		}
		c := fileChange{Path: name}
		if old, err := exec.Command("git", "--no-optional-locks", "-C", root, "cat-file", "blob", base+":"+name).Output(); err == nil {
			c.Old, c.Was = old, true
		}
		if now, err := exec.Command("git", "--no-optional-locks", "-C", root, "cat-file", "blob", tip+":"+name).Output(); err == nil {
			c.New, c.Is = now, true
		}
		out = append(out, c)
	}
	return out, nil
}
