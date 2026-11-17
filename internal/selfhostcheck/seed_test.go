package selfhostcheck

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestFormatAgreesWithSeed is the formatter's oracle since the Go formatter
// was deleted (capstone S1c, §1.4): on TestSelfhostFormatLaws's inputs,
// hostsyntax.Source gives what `ergo fmt --stdin` of the seed that
// toolchain/seed.lock names gives. The two agree when they print the same
// bytes, or when both refuse the source as a syntax error with the same code,
// line, column and message. The seed is older than the tree, so these
// outcomes are not differences:
//   - the seed refuses a source as a syntax error and the tree formats it,
//     or the seed's syntax error lies before the tree's: the source uses
//     syntax the language gained after the seed (counted and logged);
//   - the seed's or the tree's outcome holds the text of a row of
//     compiler/CHANGES-since-seed.md, which states each layout or
//     diagnostic change made since the seed.
//
// It builds and runs a binary because the question is what the last Go-built
// formatter printed: only the seed binary holds it. The seed comes from
// `go run ./scripts/bootstrap seed` (ergo's cache, else a download, else a
// build from the seed's source commit, each checked against the lock's
// digest), so the test is in the full tier. Each seed run is bounded by
// proclimit and runs in its own process group.
func TestFormatAgreesWithSeed(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: runs the seed binary")
	}
	seedBin := seedBinary(t)
	// The planted difference: `own` before an operation came after the
	// seed, so an oracle that is not the seed (one that formats with the
	// tree) fails here (fmt-seed-oracle-is-self).
	planted, err := seedFormat(seedBin, plantedNewer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(planted, "syntax ") || !strings.HasPrefix(treeOutcome(hostsyntax.Source(plantedNewer)), "ok\n") {
		t.Fatalf("the planted source %q: the seed gives %q, want a syntax failure, and the tree must format it: the oracle is not the seed", plantedNewer, planted)
	}
	seed := selfhostSeed(t)
	inputs := formatInputs(t, seed)
	allowed := changesSinceSeed(t)
	type result struct{ seed, tree string }
	results := make([]result, len(inputs))
	var wg sync.WaitGroup
	work := make(chan int)
	var mu sync.Mutex
	var runErr error
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				s, err := seedFormat(seedBin, inputs[i].Src)
				if err != nil {
					mu.Lock()
					runErr = errors.Join(runErr, fmt.Errorf("input %s: %w", inputs[i].Name, err))
					mu.Unlock()
					continue
				}
				results[i].seed = s
			}
		}()
	}
	for i := range inputs {
		work <- i
	}
	close(work)
	wg.Wait()
	if runErr != nil {
		t.Fatal(runErr)
	}
	replay := "replay: " + seedVariable + "=" + strconv.FormatUint(seed, 10) + " go test -count=1 -run '^TestFormatAgreesWithSeed$' ./internal/selfhostcheck"
	same, newer, listed, differ := 0, 0, 0, 0
	for i, in := range inputs {
		out, err := hostsyntax.Source(in.Src)
		tree := treeOutcome(out, err)
		s := results[i].seed
		switch {
		case s == tree:
			same++
		case strings.HasPrefix(s, "syntax ") && strings.HasPrefix(tree, "ok\n"),
			strings.HasPrefix(tree, "syntax ") && syntaxBefore(s, tree):
			newer++
		case listedChange(allowed, s, tree):
			listed++
		default:
			differ++
			if differ <= 3 {
				line, g, e := firstDiff(s, tree)
				t.Errorf("seed %d: input %s: the tree's formatter differs from the seed's at line %d\n  seed: %q\n  tree: %q\ninput: %q\n--- seed\n%s--- tree\n%s%s",
					seed, in.Name, line, g, e, in.Src, clip(s), clip(tree), replay)
			}
		}
	}
	t.Logf("seed %d: %d inputs: %d the same, %d in syntax newer than the seed, %d listed in compiler/CHANGES-since-seed.md, %d differ", seed, len(inputs), same, newer, listed, differ)
	if differ > 0 {
		t.Errorf("%d of %d inputs differ from the seed and are not rows of compiler/CHANGES-since-seed.md", differ, len(inputs))
	}
	if same == 0 {
		t.Error("no input formats the same on both sides: the comparison ran nothing")
	}
}

// plantedNewer is syntax the seed refuses and the tree formats.
const plantedNewer = "type S {\n\town Put(k string),\n}\n"

// seedBinary is the path of the lock's seed for this host.
func seedBinary(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "run", "./scripts/bootstrap", "seed")
	cmd.Dir = repoRoot()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go run ./scripts/bootstrap seed: %v\n%s", err, stderr.Bytes())
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		t.Fatalf("go run ./scripts/bootstrap seed printed %q, not a path", out)
	}
	return path
}

// seedFormat runs `ergo fmt --stdin` of the seed on src, bounded, in its
// own process group, and reads its outcome as treeOutcome writes it.
func seedFormat(bin, src string) (string, error) {
	cmd := exec.Command(bin, "fmt", "--stdin")
	cmd.Stdin = strings.NewReader(src)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := proclimit.Run(cmd, 1<<30)
	if err == nil {
		return "ok\n" + stdout.String(), nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		if m := seedSyntax.FindStringSubmatch(strings.TrimSuffix(stderr.String(), "\n")); m != nil && stdout.Len() == 0 {
			return "syntax " + m[1] + " " + m[2] + ":" + m[3] + " " + m[4] + "\n", nil
		}
	}
	return "", fmt.Errorf("seed: %v\nstdout: %q\nstderr: %q", err, stdout.String(), stderr.String())
}

// syntaxBefore reports whether syntax failure a lies before syntax failure b
// in the source: the seed stopped at an earlier construct the tree reads.
func syntaxBefore(a, b string) bool {
	at := func(s string) (int, int, bool) {
		f := strings.Fields(s)
		if len(f) < 3 || f[0] != "syntax" {
			return 0, 0, false
		}
		l, c, ok := strings.Cut(f[2], ":")
		line, err1 := strconv.Atoi(l)
		col, err2 := strconv.Atoi(c)
		return line, col, ok && err1 == nil && err2 == nil
	}
	al, ac, aok := at(a)
	bl, bc, bok := at(b)
	return aok && bok && (al < bl || al == bl && ac < bc)
}

// seedSyntax is the seed's report of a source that does not parse.
var seedSyntax = regexp.MustCompile(`(?s)^ergo fmt: EFMT_SYNTAX: source cannot be formatted: (\S+) at (\d+):(\d+): (.*)$`)

// treeOutcome is the tree's formatter's result in the seed's terms: "ok\n"
// and the text, or the located syntax failure.
func treeOutcome(text string, err error) string {
	if err == nil {
		return "ok\n" + text
	}
	var se *syntax.Error
	if errors.As(err, &se) && text == "" {
		return fmt.Sprintf("syntax %s %d:%d %s\n", se.Code, se.Line, se.Column, se.Message)
	}
	return "tree error " + err.Error() + "\n"
}

// changesSinceSeed reads the texts of compiler/CHANGES-since-seed.md's
// table: the first cell of each row, in backquotes.
func changesSinceSeed(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(), "compiler", "CHANGES-since-seed.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		cells := strings.Split(sc.Text(), "|")
		if len(cells) < 3 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`") {
			continue
		}
		out = append(out, strings.Trim(strings.TrimSpace(cells[1]), "`"))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// listedChange reports whether the seed's outcome or the tree's holds the
// text of a row.
func listedChange(rows []string, seed, tree string) bool {
	for _, r := range rows {
		if r != "" && (strings.Contains(seed, r) || strings.Contains(tree, r)) {
			return true
		}
	}
	return false
}
