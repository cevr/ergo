// Command lawloc-check runs and checks the LAWLOC migration (types.md, Laws
// 15): laws move beside their subjects with `ergo fix`, and the move must
// change no law and no verdict. Run it from the repository root:
//
//	go run ./scripts/lawloc-check snapshot OUT   # before the move
//	go run ./scripts/lawloc-check migrate        # ergo fix over std and examples
//	go run ./scripts/lawloc-check snapshot OUT2  # after the move
//	go run ./scripts/lawloc-check compare OUT OUT2
//
// snapshot builds ergo from the tree and writes every law of examples/ and
// internal/sketch/std by package and name, then each example module's
// `ergo test` lines (timings dropped, sorted within each package), so a
// law's place in a file does not matter. migrate builds ergo, runs `ergo
// fix --std internal/sketch/std`, builds again (std is embedded, so the
// examples check against the moved std) and runs `ergo fix` over each
// example module; it fails when a file named for laws is left, or a law
// names nothing in its package. compare exits 1 and prints the lines that
// differ. The law ledgers (ergo.laws) are files: `git diff` shows them.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "snapshot":
		if len(os.Args) != 3 {
			usage()
		}
		err = snapshot(os.Args[2])
	case "migrate":
		err = migrate()
	case "compare":
		if len(os.Args) != 4 {
			usage()
		}
		err = compare(os.Args[2], os.Args[3])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lawloc-check:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lawloc-check snapshot OUT | migrate | compare BEFORE AFTER")
	os.Exit(2)
}

// build writes the tree's ergo into a temporary directory.
func build() (string, error) {
	dir, err := os.MkdirTemp("", "lawloc-check-")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "ergo")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ergo")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build ./cmd/ergo: %v", err)
	}
	return bin, nil
}

// modules are the example programs: the directories of examples/ with an
// ergo.mod.
func modules() ([]string, error) {
	entries, err := os.ReadDir("examples")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join("examples", e.Name(), "ergo.mod")); e.IsDir() && err == nil {
			out = append(out, filepath.Join("examples", e.Name()))
		}
	}
	return out, nil
}

// laws lists every law (top-level, member or machine) of the .ergo files
// below examples/ and internal/sketch/std, outside testdata, as
// "law DIR NAME", sorted.
func laws() ([]string, error) {
	var out []string
	for _, root := range []string{"examples", "internal/sketch/std"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(p, ".ergo") {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			f, err := syntax.Parse(string(src))
			if err != nil {
				return fmt.Errorf("%s: %v", p, err)
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if l, ok := n.(*syntax.LawDecl); ok && l.Name != nil {
					out = append(out, "law "+filepath.Dir(p)+" "+l.Name.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(out)
	return out, nil
}

var timing = regexp.MustCompile(` \([0-9.]+(ns|µs|ms|s|m)\)`)

func snapshot(out string) error {
	bin, err := build()
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(bin))
	lines, err := laws()
	if err != nil {
		return err
	}
	mods, err := modules()
	if err != nil {
		return err
	}
	for _, m := range mods {
		cmd := exec.Command(bin, "test", "./...")
		cmd.Dir = m
		var buf bytes.Buffer
		cmd.Stdout, cmd.Stderr = &buf, &buf
		code := 0
		if err := cmd.Run(); err != nil {
			code = 1
			if x, ok := err.(*exec.ExitError); ok {
				code = x.ExitCode()
			}
		}
		pkg := "."
		var block []string
		flush := func() {
			slices.Sort(block)
			lines = append(lines, block...)
			block = nil
		}
		sc := bufio.NewScanner(&buf)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			l := timing.ReplaceAllString(sc.Text(), "")
			if p, ok := strings.CutPrefix(l, "package "); ok {
				flush()
				pkg = p
			}
			block = append(block, "test "+m+" "+pkg+" | "+l)
		}
		flush()
		lines = append(lines, fmt.Sprintf("test %s exit %d", m, code))
		fmt.Fprintf(os.Stderr, "%s: exit %d\n", m, code)
	}
	return os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func run(bin string, dir string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	os.Stdout.Write(out)
	if err != nil {
		return string(out), fmt.Errorf("%s %s in %s: %v", bin, strings.Join(args, " "), dir, err)
	}
	return string(out), nil
}

func migrate() error {
	bin, err := build()
	if err != nil {
		return err
	}
	all, err := run(bin, ".", "fix", "--std", "internal/sketch/std")
	if err != nil {
		return err
	}
	os.RemoveAll(filepath.Dir(bin))
	if bin, err = build(); err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(bin))
	mods, err := modules()
	if err != nil {
		return err
	}
	for _, m := range mods {
		out, err := run(bin, m, "fix", "./...")
		if err != nil {
			return err
		}
		all += out
	}
	var bad []string
	for _, l := range strings.Split(all, "\n") {
		if strings.Contains(l, "names nothing") {
			bad = append(bad, l)
		}
	}
	for _, root := range []string{"examples", "internal/sketch/std"} {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ergo") && strings.Contains(strings.ToLower(d.Name()), "law") {
				bad = append(bad, "a file of laws is left: "+p)
			}
			return nil
		})
	}
	if len(bad) > 0 {
		return fmt.Errorf("the migration is not done:\n\t%s", strings.Join(bad, "\n\t"))
	}
	return nil
}

func compare(a, b string) error {
	read := func(p string) (map[string]int, error) {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		m := map[string]int{}
		for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			m[l]++
		}
		return m, nil
	}
	before, err := read(a)
	if err != nil {
		return err
	}
	after, err := read(b)
	if err != nil {
		return err
	}
	var diff []string
	for l, n := range before {
		if after[l] < n {
			diff = append(diff, "- "+l)
		}
	}
	for l, n := range after {
		if before[l] < n {
			diff = append(diff, "+ "+l)
		}
	}
	slices.Sort(diff)
	if len(diff) > 0 {
		return fmt.Errorf("%s and %s differ:\n%s", a, b, strings.Join(diff, "\n"))
	}
	fmt.Printf("same: %d lines\n", len(before))
	return nil
}
