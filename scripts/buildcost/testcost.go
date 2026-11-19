package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// The test cost receipt (test-speed plan §1.1 and §5, unit FT0):
//
//	go run ./scripts/buildcost test [-work DIR] [-ergo PATH] [-packages example/pkg,...]
//
// For each example package, in a copy of its example module with an Ergo
// cache of its own: `ergo check` twice; a test (testcostValue, which one
// added test calls) appended to the package's first source file; `ergo
// test` first run; rerun with no change, twice; a comment edit; a one-line
// body edit (testcostValue's 1 becomes 2); `ergo check` after the edit.
// Then, per example module, `ergo test ./...` first, rerun, and after a body
// edit in the first package. Each line prints the user-mode instructions of
// the whole process tree and the stage counters, the test ones included
// (obligations_evaluated, eval_steps, obligations_native,
// obligations_stored). The summary prints the §5 rows this head can fill.

// testPackages are §1.2's packages and deps/semver (P-match).
const testPackages = "pure-eval/limit,router-model/route,deps/graph,deps/resolve,deps/semver,calc/eval,deploys/deploys,jobq/pool,regex/engine"

// testShown are the counters a test scenario line prints.
var testShown = []string{
	"files_parsed", "packages_checked", "modules_lowered", "go_compiles", "links", "processes",
	"obligations_evaluated", "eval_steps", "obligations_native", "obligations_stored",
}

func testCost(args []string) int {
	fs := flag.NewFlagSet("buildcost test", flag.ContinueOnError)
	work := fs.String("work", "", "scratch directory (default: a fresh one under /tmp/opencode, removed at the end)")
	keep := fs.Bool("keep", false, "keep -work")
	ergo := fs.String("ergo", "", "the ergo binary (default: go build ./cmd/ergo into -work)")
	packages := fs.String("packages", testPackages, "example packages, example/package, comma separated")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r, code := setUp(*work, *ergo, *keep, true)
	if code != 0 {
		return code
	}
	defer r.cleanUp()
	load, _ := os.ReadFile("/proc/loadavg")
	fmt.Printf("# buildcost test GOMAXPROCS=%s load %s", os.Getenv("GOMAXPROCS"), firstField(string(load)))
	fmt.Printf("# counted: %v\n", instructionsCounted())
	rows := map[string][]float64{}
	add := func(row string, l line) {
		if l.exit == 0 || l.exit == 1 {
			rows[row] = append(rows[row], l.instrM)
		}
	}
	modules := map[string]bool{}
	for _, spec := range strings.Split(*packages, ",") {
		example, pkg, ok := strings.Cut(strings.TrimSpace(spec), "/")
		if !ok {
			continue
		}
		dir := filepath.Join(r.work, "p", example+"-"+filepath.Base(pkg))
		if err := os.CopyFS(dir, os.DirFS(filepath.Join("examples", example))); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		file, err := firstSource(filepath.Join(dir, pkg))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		label, xdg, folder := spec, filepath.Join(dir+".xdg"), filepath.Join(dir, pkg)
		r.testLine(label+" check-1", xdg, folder, "check", folder)
		r.testLine(label+" check-2", xdg, folder, "check", folder)
		appendText(file, "\nfunc testcostValue() int {\n\t1\n}\n\ntest TestcostCalls {\n\tassert testcostValue() > 0\n}\n")
		add("7/8 first run", r.testLine(label+" test-first", xdg, folder, "test", folder))
		r.testLine(label+" test-rerun-1", xdg, folder, "test", folder)
		add("1 rerun, no change", r.testLine(label+" test-rerun-2", xdg, folder, "test", folder))
		appendText(file, "\n// a testcost comment edit\n")
		add("3 comment edit", r.testLine(label+" test-comment-edit", xdg, folder, "test", folder))
		replaceText(file, "func testcostValue() int {\n\t1\n}", "func testcostValue() int {\n\t2\n}")
		add("4/5 body edit", r.testLine(label+" test-body-edit", xdg, folder, "test", folder))
		r.testLine(label+" check-after-edit", xdg, folder, "check", folder)
		if !modules[example] {
			modules[example] = true
			mdir := filepath.Join(r.work, "m", example)
			if err := os.CopyFS(mdir, os.DirFS(filepath.Join("examples", example))); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			mfile, err := firstSource(filepath.Join(mdir, pkg))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			mxdg := mdir + ".xdg"
			r.testLine(example+"/... test-first", mxdg, mdir, "test", "./...")
			r.testLine(example+"/... test-rerun", mxdg, mdir, "test", "./...")
			appendText(mfile, "\nfunc testcostModule() int {\n\t1\n}\n")
			add("11 module, one body edit", r.testLine(example+"/... test-body-edit", mxdg, mdir, "test", "./..."))
		}
	}
	fmt.Println("# test-speed plan §5, this head (M instructions, min to max over the packages):")
	names := mapKeys(rows)
	slices.Sort(names)
	for _, row := range names {
		v := rows[row]
		fmt.Printf("#   row %-26s %.1f to %.1f (%d packages)\n", row, slices.Min(v), slices.Max(v), len(v))
	}
	return 0
}

// line is one measured scenario.
type line struct {
	exit   int
	instrM float64
}

// testLine runs ergo with args in dir and prints its line.
func (r runner) testLine(label, xdg, dir string, args ...string) line {
	report := filepath.Join(r.work, "stage.json")
	os.Remove(report)
	cmd := exec.Command(r.ergo, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg, "ERGO_STAGE_REPORT="+report)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = nil, &stderr
	res := measure(cmd)
	counts := map[string]int64{}
	if data, err := os.ReadFile(report); err == nil {
		json.Unmarshal(data, &counts)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-36s exit=%d instr_M=%.1f", label, res.exit, float64(res.instructions)/1e6)
	for _, k := range testShown {
		fmt.Fprintf(&b, " %s=%d", k, counts[k])
	}
	fmt.Fprintf(&b, " | user_s=%.2f sys_s=%.2f wall_ms=%.1f", res.user.Seconds(), res.sys.Seconds(), float64(res.wall.Microseconds())/1000)
	fmt.Println(b.String())
	if res.exit != 0 && res.exit != 1 {
		fmt.Printf("    stderr: %s\n", firstLine(stderr.String()))
	}
	return line{exit: res.exit, instrM: float64(res.instructions) / 1e6}
}

// firstSource is the first .ergo file of the package folder, by name.
func firstSource(dir string) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.ergo"))
	slices.Sort(matches)
	if len(matches) == 0 {
		return "", fmt.Errorf("buildcost test: %s has no .ergo file", dir)
	}
	return matches[0], nil
}

func appendText(path, text string) {
	data, _ := os.ReadFile(path)
	os.WriteFile(path, append(data, text...), 0o644)
}

func replaceText(path, from, to string) {
	data, _ := os.ReadFile(path)
	os.WriteFile(path, bytes.Replace(data, []byte(from), []byte(to), 1), 0o644)
}
