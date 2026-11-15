// Command buildcost runs the compile-speed scenarios (compile-speed plan
// §1, unit CS0) and prints one counted line for each: the compiler's own
// stage counters (ERGO_STAGE_REPORT, internal/stage) and the user-mode
// instructions of the whole process tree (perf_event_open, where the host
// allows it). Wall and CPU times are printed as context only; a receipt
// compares counts.
//
//	go run ./scripts/buildcost [-work DIR] [-ergo PATH] [-sizes 10,50] [-groups 24] [-seed 1] [-examples deps]
//	go run ./scripts/buildcost gen DIR N [SEED [GROUPS]]
//	go run ./scripts/buildcost empty [-work DIR]
//	go run ./scripts/buildcost start [-work DIR] [-ergo PATH]
//	go run ./scripts/buildcost count CMD [ARG...]
//	go run ./scripts/buildcost reuse [-work DIR] [-sizes 10,50] [-examples deps]
//
// The scenarios, for each generated program gN (internal/testinputs/
// genprogram) and each named example: first build (Go's std archives are
// warm in GOCACHE), warm, warm to a new output, comment edit, body edit in
// the lowest package, signature edit in the lowest package, body edit in
// main, `ergo check` twice, and `ergo test` of the lowest package twice.
// `empty` counts the thin client (the ergo process with no work, a small
// Go program, /bin/true) and the empty-package compile (Go's compile of
// `package p`, the fixed cost of one compile process).
// `start` counts the process start (start.go), and `count` counts any
// command: the least user-mode instructions of five runs. `reuse` counts
// in process what the incremental check would reuse after each edit
// (reuse.go).
//
// Each run has its own Ergo cache under -work (XDG_CACHE_HOME), so a
// scenario's predecessor is the one listed before it. -work defaults to a
// fresh directory under /tmp/opencode, removed at the end unless -keep.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/testinputs/genprogram"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "gen" {
		os.Exit(gen(os.Args[2:]))
	}
	if len(os.Args) > 2 && os.Args[1] == "count" {
		runner{}.leastOf(strings.Join(os.Args[2:], " "), os.Args[2:])
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "empty" || os.Args[1] == "start" || os.Args[1] == "reuse") {
		os.Exit(run(os.Args[2:], os.Args[1]))
	}
	os.Exit(run(os.Args[1:], ""))
}

// gen writes the program of N packages for SEED to DIR.
func gen(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: buildcost gen DIR N [SEED [GROUPS]]")
		return 2
	}
	n, err := strconv.Atoi(args[1])
	seed, groups := uint64(1), 24
	if err == nil && len(args) > 2 {
		seed, err = strconv.ParseUint(args[2], 10, 64)
	}
	if err == nil && len(args) > 3 {
		groups, err = strconv.Atoi(args[3])
	}
	if err != nil || n <= 0 {
		fmt.Fprintln(os.Stderr, "buildcost gen: N, SEED and GROUPS are positive numbers")
		return 2
	}
	if err := writeProgram(args[0], genprogram.GenerateProgram(seed, n, genprogram.Options{Groups: groups})); err != nil {
		fmt.Fprintln(os.Stderr, "buildcost gen:", err)
		return 1
	}
	return 0
}

func writeProgram(dir string, p genprogram.Program) error {
	names := mapKeys(p.Files)
	slices.Sort(names)
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(p.Files[name]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// shown are the counters a scenario line prints, in order.
var shown = []string{
	"files_read", "bytes_read", "files_parsed", "std_files_parsed", "packages_checked",
	"std_packages_decoded", "modules_lowered", "go_bytes_lowered", "modules_reused", "bytes_hashed",
	"go_compiles", "go_compiles_reused", "links", "links_reused", "go_commands", "processes", "bytes_written",
}

type runner struct {
	work, ergo string
	keep       bool
}

func run(args []string, mode string) int {
	fs := flag.NewFlagSet("buildcost", flag.ContinueOnError)
	work := fs.String("work", "", "scratch directory (default: a fresh one under /tmp/opencode, removed at the end)")
	keep := fs.Bool("keep", false, "keep -work")
	ergo := fs.String("ergo", "", "the ergo binary (default: go build ./cmd/ergo into -work)")
	sizes := fs.String("sizes", "10,50", "generated program sizes, in packages")
	groups := fs.Int("groups", 24, "function groups per generated package (the seed draws 1 to GROUPS)")
	seed := fs.Uint64("seed", 1, "generator seed")
	examples := fs.String("examples", "deps", "example programs under examples/ to measure (comma separated; empty for none)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := runner{work: *work, ergo: *ergo, keep: *keep}
	if r.work == "" {
		if err := os.MkdirAll("/tmp/opencode", 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		dir, err := os.MkdirTemp("/tmp/opencode", "buildcost-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		r.work = dir
		if !r.keep {
			defer os.RemoveAll(dir)
		}
	}
	if r.ergo == "" && mode != "reuse" {
		r.ergo = filepath.Join(r.work, "bin", "ergo")
		build := exec.Command("go", "build", "-o", r.ergo, "./cmd/ergo")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "buildcost: go build ./cmd/ergo:", err)
			return 1
		}
	}
	load, _ := os.ReadFile("/proc/loadavg")
	fmt.Printf("# buildcost %s/%s GOMAXPROCS=%d load %s", runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), firstField(string(load)))
	fmt.Printf("# counted: %v\n", instructionsCounted())
	switch mode {
	case "empty":
		return r.empty()
	case "start":
		return r.start()
	}
	for _, s := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n <= 0 {
			continue
		}
		dir := filepath.Join(r.work, fmt.Sprintf("g%d", n))
		p := genprogram.GenerateProgram(*seed, n, genprogram.Options{Groups: *groups})
		if err := writeProgram(dir, p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if mode == "reuse" {
			r.reuse(fmt.Sprintf("g%d", n), dir, "cmd/app", genprogram.Name(0)+"/"+genprogram.Name(0)+".ergo")
			continue
		}
		r.scenarios(fmt.Sprintf("g%d", n), dir, "cmd/app", genprogram.Name(0)+"/"+genprogram.Name(0)+".ergo", genprogram.Name(0))
	}
	for _, name := range strings.Split(*examples, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		dir := filepath.Join(r.work, name)
		if err := os.CopyFS(dir, os.DirFS(filepath.Join("examples", name))); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		main, leaf, err := exampleLayout(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if mode == "reuse" {
			r.reuse(name, dir, main, leaf)
			continue
		}
		r.scenarios(name, dir, main, leaf, filepath.Dir(leaf))
	}
	return 0
}

// exampleLayout finds an example's main package (the folder of its
// cmd/<name>/main.ergo, or its root) and a non-main .ergo file to edit.
func exampleLayout(dir string) (main, leaf string, err error) {
	main = "."
	matches, _ := filepath.Glob(filepath.Join(dir, "cmd", "*", "main.ergo"))
	if len(matches) > 0 {
		main, _ = filepath.Rel(dir, filepath.Dir(matches[0]))
	}
	err = filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil || leaf != "" {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if !e.IsDir() && strings.HasSuffix(rel, ".ergo") && filepath.Dir(rel) != main && !strings.HasPrefix(rel, "cmd/") && filepath.Dir(rel) != "." {
			leaf = rel
		}
		return nil
	})
	if err == nil && leaf == "" {
		err = fmt.Errorf("buildcost: %s has no package besides main", dir)
	}
	return main, leaf, err
}

// scenarios runs the scenario list on the program at dir; main is its
// main package, leaf a file of its lowest package, leafPkg that package.
func (r runner) scenarios(label, dir, main, leaf, leafPkg string) {
	xdg := filepath.Join(r.work, "xdg-"+label)
	out := filepath.Join(r.work, "out", label)
	pkg := filepath.Join(dir, main)
	edit := func(from, to string) {
		path := filepath.Join(dir, leaf)
		data, _ := os.ReadFile(path)
		os.WriteFile(path, bytes.Replace(data, []byte(from), []byte(to), 1), 0o644)
	}
	appendTo := func(path, text string) {
		data, _ := os.ReadFile(path)
		os.WriteFile(path, append(data, text...), 0o644)
	}
	r.counted(label+" first", xdg, "build", pkg, "-o", out)
	r.counted(label+" warm", xdg, "build", pkg, "-o", out)
	r.counted(label+" warm-new-output", xdg, "build", pkg, "-o", out+"2")
	appendTo(filepath.Join(dir, leaf), "\n// a comment edit\n")
	r.counted(label+" comment-edit", xdg, "build", pkg, "-o", out)
	if strings.HasPrefix(label, "g") {
		edit("3 * c.R * c.R", "4 * c.R * c.R")
	} else {
		appendTo(filepath.Join(dir, leaf), "\nfunc buildcostBody() int {\n\t1\n}\n")
	}
	r.counted(label+" leaf-body-edit", xdg, "build", pkg, "-o", out)
	appendTo(filepath.Join(dir, leaf), "\n// BuildcostAdded is a signature edit.\nfunc BuildcostAdded(n int) int {\n\tn + 1\n}\n")
	r.counted(label+" leaf-signature-edit", xdg, "build", pkg, "-o", out)
	appendTo(filepath.Join(pkg, "main.ergo"), "\nfunc buildcostMain() int {\n\t2\n}\n")
	r.counted(label+" main-body-edit", xdg, "build", pkg, "-o", out)
	r.counted(label+" check-1", xdg, "check", pkg)
	r.counted(label+" check-2", xdg, "check", pkg)
	r.counted(label+" test-leaf-1", xdg, "test", filepath.Join(dir, leafPkg))
	r.counted(label+" test-leaf-2", xdg, "test", filepath.Join(dir, leafPkg))
}

// counted runs ergo with args and prints its line.
func (r runner) counted(label, xdg string, args ...string) {
	report := filepath.Join(r.work, "stage.json")
	os.Remove(report)
	cmd := exec.Command(r.ergo, args...)
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+xdg, "ERGO_STAGE_REPORT="+report)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = nil, &stderr
	res := measure(cmd)
	counts := map[string]int64{}
	if data, err := os.ReadFile(report); err == nil {
		json.Unmarshal(data, &counts)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-28s exit=%d", label, res.exit)
	if res.instructions > 0 {
		fmt.Fprintf(&b, " instr_M=%.1f", float64(res.instructions)/1e6)
	}
	for _, k := range shown {
		fmt.Fprintf(&b, " %s=%d", k, counts[k])
	}
	fmt.Fprintf(&b, " | user_s=%.2f sys_s=%.2f wall_ms=%.1f", res.user.Seconds(), res.sys.Seconds(), float64(res.wall.Microseconds())/1000)
	fmt.Println(b.String())
	if res.exit != 0 && res.exit != 1 {
		fmt.Printf("    stderr: %s\n", firstLine(stderr.String()))
	}
}

// empty counts the thin client and the empty-package compile.
func (r runner) empty() int {
	for _, c := range []struct {
		label string
		argv  []string
	}{
		{"thin-client ergo (usage, no work)", []string{r.ergo}},
		{"/bin/true", []string{"/bin/true"}},
	} {
		r.plain(c.label, c.argv)
	}
	small := filepath.Join(r.work, "small")
	os.MkdirAll(small, 0o755)
	os.WriteFile(filepath.Join(small, "go.mod"), []byte("module small\n\ngo 1.24\n"), 0o644)
	os.WriteFile(filepath.Join(small, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	build := exec.Command("go", "build", "-o", filepath.Join(r.work, "small.bin"), ".")
	build.Dir = small
	if err := build.Run(); err == nil {
		r.plain("small Go program (empty main)", []string{filepath.Join(r.work, "small.bin")})
	}
	tool, err := exec.Command("go", "env", "GOTOOLDIR").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "buildcost: go env GOTOOLDIR:", err)
		return 1
	}
	compile := filepath.Join(strings.TrimSpace(string(tool)), "compile")
	empty := filepath.Join(r.work, "empty")
	os.MkdirAll(empty, 0o755)
	os.WriteFile(filepath.Join(empty, "p.go"), []byte("package p\n"), 0o644)
	os.WriteFile(filepath.Join(empty, "importcfg"), []byte("# import config\n"), 0o644)
	for i := range 3 {
		r.plain(fmt.Sprintf("empty-package compile %d", i+1), []string{compile, "-o", filepath.Join(empty, "p.a"), "-p", "p", "-pack", "-complete", "-importcfg", filepath.Join(empty, "importcfg"), filepath.Join(empty, "p.go")})
	}
	return 0
}

func (r runner) plain(label string, argv []string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	res := measure(cmd)
	fmt.Printf("%-36s exit=%d instr_M=%.2f | user_s=%.3f sys_s=%.3f wall_ms=%.2f\n", label, res.exit, float64(res.instructions)/1e6, res.user.Seconds(), res.sys.Seconds(), float64(res.wall.Microseconds())/1000)
}

// result is one measured run.
type result struct {
	exit         int
	instructions uint64 // 0 when the host does not count
	user, sys    time.Duration
	wall         time.Duration
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return "?\n"
	}
	return f[0] + "\n"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
