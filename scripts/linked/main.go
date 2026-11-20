// Command linked writes the Go of the Ergo packages the ergo binary links
// (internal/linked): for each row of linked.Packages, the command lines it
// generates and its lowered Go package. Run it from the repository root
// after an edit of a linked module, of the std packages it imports, or of
// the compiler; TestLinkedGoIsFresh fails until it has run.
//
//	go run ./scripts/linked
//
// The ergo binary parses with the linked compiler/syntax, so a change of
// its node layout (syntax.ColumnsVersion) is bootstrapped in two stages:
//
//	go run ./scripts/linked -bootstrap
//
// Stage 1 regenerates with the linked parser one layout behind, which the
// decoder still reads (syntax.PreviousColumnsVersion). A std or compiler
// line in new syntax that parser refuses is held back for stage 1 only:
// it is commented out on disk, and put back when stage 1 ends. Stage 2
// then regenerates with the parser stage 1 wrote. Without -bootstrap, a
// linked parser one layout behind is refused with this instruction, never
// decoded at the wrong width.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/linked"
	"ergo.local/prototype/internal/sketch/syntax"
)

var (
	bootstrap = flag.Bool("bootstrap", false, "regenerate across a node-layout change, in two stages")
	stage1    = flag.Bool("stage1", false, "stage 1 of -bootstrap: regenerate with the linked parser one layout behind")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "linked:", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("run it from the repository root: %w", err)
	}
	layout, err := syntax.LinkedLayout()
	if err != nil {
		return err
	}
	behind := layout == syntax.PreviousColumnsVersion
	switch {
	case layout != syntax.ColumnsVersion && !behind:
		return &syntax.LayoutError{Linked: layout}
	case behind && *bootstrap:
		return twoStages(root)
	case behind && !*stage1:
		return fmt.Errorf("the linked parser writes node layout %d and this tree's compiler/syntax layout %d: a layout change is bootstrapped in two stages: go run ./scripts/linked -bootstrap", layout, syntax.ColumnsVersion)
	case !behind && *bootstrap:
		fmt.Fprintln(os.Stderr, "linked: the linked parser already writes this tree's layout; regenerating once")
	}
	return generate(root)
}

func generate(root string) error {
	all := map[string][]byte{}
	for _, p := range linked.Packages {
		files, err := linked.Generate(root, p)
		if err != nil {
			return err
		}
		for _, rel := range linked.Sorted(files) {
			full := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, files[rel], 0o644); err != nil {
				return err
			}
			all[rel] = files[rel]
			fmt.Println(rel)
		}
	}
	// The stamp last: its inputs digest covers the generated command
	// lines just written into the modules.
	inputs, err := linked.InputsDigest(root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(linked.StampFile)), linked.Stamp(inputs, all), 0o644); err != nil {
		return err
	}
	fmt.Println(linked.StampFile)
	return nil
}

// twoStages holds back what the linked parser refuses, runs stage 1 as a
// fresh build (std is embedded, so the held sources must be compiled in),
// puts the sources back, and runs stage 2 with the parser stage 1 wrote.
func twoStages(root string) error {
	held, err := holdBack(root)
	restore := func() {
		for path, src := range held {
			if werr := os.WriteFile(path, []byte(src), 0o644); werr != nil {
				fmt.Fprintln(os.Stderr, "linked: put back", path, werr)
			}
		}
	}
	if err != nil {
		restore()
		return err
	}
	for path := range held {
		fmt.Fprintln(os.Stderr, "linked: stage 1 holds back lines of", path)
	}
	err = goRun(root, "-stage1")
	restore()
	if err != nil {
		return fmt.Errorf("stage 1: %w", err)
	}
	if err := goRun(root); err != nil {
		return fmt.Errorf("stage 2: %w", err)
	}
	return nil
}

func goRun(root string, args ...string) error {
	cmd := exec.Command("go", append([]string{"run", "./scripts/linked"}, args...)...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
	return cmd.Run()
}

// heldMark starts a line stage 1 holds back.
const heldMark = "// held back by scripts/linked stage 1: "

// holdBack comments out, in the Ergo sources the linked packages read (std
// and compiler, not their testdata), each line the linked parser refuses,
// until every file parses. It answers each changed file's original text.
func holdBack(root string) (map[string]string, error) {
	held := map[string]string{}
	for _, dir := range []string{"internal/sketch/std", "compiler"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && e.Name() == "testdata" {
				return filepath.SkipDir // fixtures refuse syntax on purpose
			}
			if e.IsDir() || !strings.HasSuffix(path, ".ergo") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			orig := string(b)
			src := orig
			for {
				_, perr := syntax.ParseRows(src)
				if perr == nil {
					break
				}
				var se *syntax.Error
				if !errors.As(perr, &se) || se.Line < 1 {
					return fmt.Errorf("%s: %w", path, perr)
				}
				lines := strings.Split(src, "\n")
				l := lines[se.Line-1]
				if strings.HasPrefix(l, heldMark) {
					return fmt.Errorf("%s:%d: the linked parser still refuses it after its line is held back (a declaration over several lines): %w", path, se.Line, perr)
				}
				lines[se.Line-1] = heldMark + l
				src = strings.Join(lines, "\n")
			}
			if src != orig {
				held[path] = orig
				return os.WriteFile(path, []byte(src), 0o644)
			}
			return nil
		})
		if err != nil {
			return held, err
		}
	}
	return held, nil
}
