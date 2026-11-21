// Command downstream is ergo's downstream check (docs/tooling.md,
// Downstream check): it builds pinned commits of the repositories that
// ergo must not break, with the checkout's own compiler, as Rust's
// cargotest builds pinned crates in rustc's CI.
//
//	go run ./scripts/downstream fetch [NAME...]   bring each pinned commit into the cache (network)
//	go run ./scripts/downstream [NAME...]         check each pin from the cache (no network)
//
// downstream/pins holds one `NAME URL COMMIT` line per repository. A check
// copies the cached tree to scratch and runs, with -ergo (bin/ergo of
// -root by default, never an ergo found on PATH): `ergo check ./...`,
// `ergo test ./...` (its skip lines must equal the tree's expected-skips),
// `ergo build` of each cmd/NAME, and `ergo test ./cmd/NAME --cases
// cmd/NAME/cases` where the folder exists. A pin that is not in the cache
// fails and names the fetch command; it is never skipped. Exit status: 0
// when every pin is green, 1 when one is not, 2 for a usage error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/cases"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// config is a run's flags.
type config struct {
	root, ergo, cache string
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("downstream", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfg config
	flags.StringVar(&cfg.root, "root", ".", "the ergo checkout whose downstream/ files and bin/ergo are used")
	flags.StringVar(&cfg.ergo, "ergo", "bin/ergo", "the compiler; a relative `PATH` is relative to -root")
	flags.StringVar(&cfg.cache, "cache", "", "the downstream cache (default $"+cacheVariable+", else the user's cache's ergo/downstream)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	names := flags.Args()
	fetching := len(names) > 0 && names[0] == "fetch"
	if fetching {
		names = names[1:]
	}
	if cfg.cache == "" {
		dir, err := defaultCache()
		if err != nil {
			fmt.Fprintln(stderr, "downstream:", err)
			return 2
		}
		cfg.cache = dir
	}
	all, err := readPins(filepath.Join(cfg.root, pinsFile))
	if err != nil {
		fmt.Fprintln(stderr, "downstream:", err)
		return 2
	}
	pins, err := selectPins(all, names)
	if err != nil {
		fmt.Fprintln(stderr, "downstream:", err)
		return 2
	}
	if fetching {
		return fetch(stdout, cfg.cache, pins)
	}
	moved, err := movedPaths(filepath.Join(cfg.root, movedFile))
	if err != nil {
		fmt.Fprintln(stderr, "downstream:", err)
		return 2
	}
	if len(all) == 0 {
		// Before the first split (spec SPLIT §7 step 5) there is nothing
		// to pin; once a path has moved, its repository must be pinned.
		if moved > 0 {
			fmt.Fprintf(stdout, "FAIL downstream: 0 pins, but %s holds %d moved paths: pin the repositories they moved to\n", movedFile, moved)
			return 1
		}
		fmt.Fprintln(stdout, "downstream: 0 pins")
		return 0
	}
	ergo := cfg.ergo
	if !filepath.IsAbs(ergo) {
		ergo = filepath.Join(cfg.root, ergo)
	}
	if ergo, err = filepath.Abs(ergo); err != nil {
		fmt.Fprintln(stderr, "downstream:", err)
		return 2
	}
	l := cases.NewLive()
	defer l.HandleSignals(stderr, "downstream")()
	r := &runner{out: stdout, ergo: ergo, cache: cfg.cache, live: l}
	failed := 0
	for _, p := range pins {
		if !r.check(p) {
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(stdout, "downstream: FAIL (%d of %d pins)\n", failed, len(pins))
		return 1
	}
	fmt.Fprintf(stdout, "downstream: %d pins passed\n", len(pins))
	return 0
}

// runner checks pins.
type runner struct {
	out         io.Writer
	ergo, cache string
	live        *cases.Live
}

// leg is one ergo command of a pin's check, run in the copy's root.
type leg struct {
	name string
	argv []string
	// skips, when set, compares the leg's skip lines with expected-skips.
	skips bool
}

// legs are the commands that check the tree at dir: check, test, then
// the build of each binary (into bin) and its cases.
func legs(dir, bin string) ([]leg, error) {
	out := []leg{
		{name: "check", argv: []string{"check", "./..."}},
		{name: "test", argv: []string{"test", "./..."}, skips: true},
	}
	binaries, err := os.ReadDir(filepath.Join(dir, "cmd"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, b := range binaries {
		if !b.IsDir() {
			continue
		}
		pkg := "./cmd/" + b.Name()
		out = append(out, leg{name: "build " + pkg, argv: []string{"build", pkg, "-o", filepath.Join(bin, b.Name())}})
		if info, err := os.Stat(filepath.Join(dir, "cmd", b.Name(), "cases")); err == nil && info.IsDir() {
			out = append(out, leg{name: "cases " + pkg, argv: []string{"test", pkg, "--cases", "cmd/" + b.Name() + "/cases"}})
		}
	}
	return out, nil
}

// check runs every leg of p on a scratch copy of its cached tree and
// prints `ok NAME COMMIT` or a FAIL line per failed leg with its output.
func (r *runner) check(p pin) bool {
	short := p.commit[:12]
	src := entry(r.cache, p)
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		fmt.Fprintf(r.out, "FAIL %s %s: not in the cache (%s); run go run ./scripts/downstream fetch\n", p.name, short, src)
		return false
	}
	scratch, err := r.live.MakeDir("", "ergo-downstream-")
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s %s: %v\n", p.name, short, err)
		return false
	}
	defer r.live.RemoveDir(scratch)
	dir := filepath.Join(scratch, p.name)
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		fmt.Fprintf(r.out, "FAIL %s %s: copy the tree: %v\n", p.name, short, err)
		return false
	}
	all, err := legs(dir, filepath.Join(scratch, "bin"))
	if err != nil {
		fmt.Fprintf(r.out, "FAIL %s %s: %v\n", p.name, short, err)
		return false
	}
	green := true
	for _, lg := range all {
		o, err := r.live.Execute(append([]string{r.ergo}, lg.argv...), dir, os.Environ(), "", 0, 0)
		var why []string
		switch {
		case err != nil:
			why = append(why, fmt.Sprintf("could not start: %v", err))
		case o.OverCap() != "":
			why = append(why, o.OverCap())
		case !o.Passed():
			why = append(why, "exit "+o.StatusText())
		}
		if lg.skips && err == nil && o.OverCap() == "" {
			why = append(why, skipProblems(o.Stdout, dir)...)
		}
		if len(why) == 0 {
			continue
		}
		green = false
		fmt.Fprintf(r.out, "FAIL %s %s: %s: %s\n", p.name, short, lg.name, strings.Join(why, "; "))
		if err == nil && o.OverCap() == "" {
			for _, stream := range [][]byte{o.Stdout, o.Stderr} {
				for _, line := range cases.Lines(stream) {
					fmt.Fprintf(r.out, "    %s\n", line)
				}
			}
		}
	}
	if green {
		fmt.Fprintf(r.out, "ok %s %s\n", p.name, short)
	}
	return green
}

// skipProblems compares the `skip ` lines of a test run with the tree's
// expected-skips (one line each; # starts a comment; no file: none): the
// two must be equal, as a downstream's own CI checks with sort | diff.
func skipProblems(stdout []byte, dir string) []string {
	var want []string
	if data, err := os.ReadFile(filepath.Join(dir, "expected-skips")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				want = append(want, line)
			}
		}
	}
	var got []string
	for _, line := range cases.Lines(stdout) {
		if strings.HasPrefix(line, "skip ") {
			got = append(got, strings.TrimSpace(line))
		}
	}
	var problems []string
	for _, line := range got {
		if !slices.Contains(want, line) {
			problems = append(problems, "unexpected "+line)
		}
	}
	for _, line := range want {
		if !slices.Contains(got, line) {
			problems = append(problems, "expected-skips names a skip the run did not print: "+line)
		}
	}
	return problems
}
