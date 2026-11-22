package main

// Reds by failure line (GATE-REACH). A landing compares its failures with
// the base's: a test that was red on main hid every new line in it while
// the comparison went by test name (TestStdGoShareOnlyFalls took on hmac,
// totp, json, sql and term rows one tip at a time; 12 of the 15 tests red
// on main 5cce5f8c6 had been seen red and landed). Each run of a clean
// checkout records its red lines under its tree and tier in the gate's
// cache; -new-reds BASE reads the record of BASE's tree for the same tier
// and fails on any line BASE does not have. The record is a run's result,
// never a list anyone edits: a BASE with no record is an error, and the
// fix is a run at BASE.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// redsSubdir holds the records in the gate's cache directory, one file per
// tree and tier.
const redsSubdir = "reds"

// redSet is a run's red lines, each counted: a line that fails twice is
// two lines, so a second failure of the same kind is new.
type redSet map[string]int

// redStep is the writer a failed step's output goes through: each line,
// normalised, is one red line of the step (redKey).
type redStep struct {
	set  redSet
	step string
	test string
	part []byte
}

// redsOf starts collecting the lines of the failed step name into set.
func redsOf(set redSet, name string) *redStep {
	return &redStep{set: set, step: stepFamily(name)}
}

func (w *redStep) Write(p []byte) (int, error) {
	w.part = append(w.part, p...)
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.line(string(w.part[:i]))
		w.part = w.part[i+1:]
	}
}

// Close takes the last line, which has no newline.
func (w *redStep) Close() error {
	if len(w.part) > 0 {
		w.line(string(w.part))
		w.part = nil
	}
	return nil
}

// line is one output line of the failed step. A "--- FAIL: T" line names
// the test the lines after it belong to and is a red line itself, so a
// test that fails with no message still counts.
func (w *redStep) line(text string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == "FAIL" || strings.HasPrefix(trimmed, "=== ") {
		return
	}
	if name, ok := strings.CutPrefix(trimmed, "--- FAIL: "); ok {
		name, _, _ = strings.Cut(name, " ")
		w.test = name
		w.set[redKey(w.step, name, "FAIL")]++
		return
	}
	if strings.HasPrefix(trimmed, "FAIL\t") || strings.HasPrefix(trimmed, "ok  \t") {
		// A package's summary line: its time is not a failure.
		w.test = ""
		return
	}
	w.set[redKey(w.step, w.test, trimmed)]++
}

// redKey is a red line as the comparison sees it: the step's family, the
// test, and the line with what moves between runs of the same failure
// taken out (normalise).
func redKey(step, test, line string) string {
	return step + "\t" + test + "\t" + normalise(line)
}

// stepFamily is a step's name without its shard ("[2/5]"): a new test
// moves others between shards.
func stepFamily(name string) string {
	if i := strings.LastIndex(name, " ["); i >= 0 && strings.HasSuffix(name, "]") {
		return name[:i]
	}
	return name
}

var (
	// file.go:45: and file.go:45:6: (I22 counted one line twice when it
	// moved from :45 to :46).
	redPositions = regexp.MustCompile(`(\.[A-Za-z0-9]+):\d+(:\d+)?`)
	// (0.20s), 1.5ms: a time.
	redTimes = regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms|s|m|h)\b`)
	// Temporary directories and digests.
	redDigits = regexp.MustCompile(`\d{6,}`)
	redHex    = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
)

// normalise takes positions, times, long numbers and digests out of a
// failure line.
func normalise(line string) string {
	line = redPositions.ReplaceAllString(line, "$1:N")
	line = redHex.ReplaceAllString(line, "H")
	line = redDigits.ReplaceAllString(line, "N")
	return redTimes.ReplaceAllString(line, "T")
}

// redsPath is the record of the run of tree in tier.
func redsPath(cacheDir, tree, tier string) string {
	return filepath.Join(cacheDir, redsSubdir, tree+"."+tierFile(tier))
}

// tierFile is the tier's name in a record's file name.
func tierFile(tier string) string {
	if tier == "" {
		return "default"
	}
	return strings.NewReplacer("/", "-", " ", "-").Replace(tier)
}

// writeReds records set as the run of tree in tier: one line per red
// line, sorted, with its count.
func writeReds(cacheDir, tree, tier string, set redSet) error {
	path := redsPath(cacheDir, tree, tier)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, key := range slices.Sorted(maps.Keys(set)) {
		fmt.Fprintf(&b, "%d\t%s\n", set[key], key)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// readReds reads a record writeReds wrote.
func readReds(path string) (redSet, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	set := redSet{}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		count, key, ok := strings.Cut(scanner.Text(), "\t")
		n := 0
		if _, err := fmt.Sscanf(count, "%d", &n); !ok || err != nil || n <= 0 {
			return nil, fmt.Errorf("%s: a line that is no count and red line: %q", path, scanner.Text())
		}
		set[key] += n
	}
	return set, scanner.Err()
}

// compareReds is what head has that base has not (new) and what base has
// that head has not (fixed), each line as often as its counts differ.
func compareReds(base, head redSet) (added, fixed []string) {
	for key, n := range head {
		for range n - base[key] {
			added = append(added, key)
		}
	}
	for key, n := range base {
		for range n - head[key] {
			fixed = append(fixed, key)
		}
	}
	slices.Sort(added)
	slices.Sort(fixed)
	return added, fixed
}

// reportReds prints the comparison with the base's record and returns
// the gate's exit code: 1 on a new line, else 0, whatever the base had
// red; 2 when the base has no record.
func reportReds(out io.Writer, root, cacheDir, base, tier string, head redSet) int {
	command := exec.Command("git", "--no-optional-locks", "rev-parse", base+"^{tree}")
	command.Dir = root
	tree, err := runOutput(command)
	if err != nil {
		fmt.Fprintf(out, "gate: -new-reds %s: %v\n", base, err)
		return 2
	}
	path := redsPath(cacheDir, strings.TrimSpace(string(tree)), tier)
	baseSet, err := readReds(path)
	if err != nil {
		fmt.Fprintf(out, "gate: -new-reds %s: no record of a run of its tree in this tier (%v): run the same tier on a clean checkout of %s first\n", base, err, base)
		return 2
	}
	added, fixed := compareReds(baseSet, head)
	for _, key := range added {
		fmt.Fprintf(out, "reds: NEW   %s\n", strings.ReplaceAll(key, "\t", " | "))
	}
	for _, key := range fixed {
		fmt.Fprintf(out, "reds: FIXED %s\n", strings.ReplaceAll(key, "\t", " | "))
	}
	fmt.Fprintf(out, "reds: %d new, %d fixed against %s (%s)\n", len(added), len(fixed), base, path)
	if len(added) > 0 {
		return 1
	}
	return 0
}
