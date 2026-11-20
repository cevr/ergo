// Command linked writes the Go of the Ergo packages the ergo binary links
// (internal/linked): for each row of linked.Packages, the command lines it
// generates and its lowered Go package. Run it from the repository root
// after an edit of a linked module, of the std packages it imports, or of
// the compiler; TestLinkedGoIsFresh fails until it has run.
//
//	go run ./scripts/linked
//
// The ergo binary parses with the linked compiler/syntax, so a tree the
// linked parser cannot read is bootstrapped in two stages:
//
//	go run ./scripts/linked -bootstrap
//
// The tree needs it when the linked parser cannot parse it: its node
// layout is one behind this tree's (syntax.PreviousColumnsVersion, which
// the decoder still reads), or it refuses a line of the std or compiler
// sources the linked packages read (new syntax on the same layout, such
// as a new token). Stage 1 regenerates with the linked parser, each line
// it refuses held back: commented out on disk, and put back when stage 1
// ends. Stage 2 then regenerates with the parser stage 1 wrote. -bootstrap
// on a tree the linked parser reads regenerates once. Without -bootstrap,
// a tree that needs two stages is refused with this instruction: never
// decoded at the wrong width, never regenerated without its new syntax.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	if layout != syntax.ColumnsVersion && !behind {
		return &syntax.LayoutError{Linked: layout}
	}
	held, err := refused(root)
	if err != nil {
		return err
	}
	two, err := stages(layout, held, *bootstrap, *stage1)
	if err != nil {
		return err
	}
	if two {
		return twoStages(root, held)
	}
	if *bootstrap {
		fmt.Fprintln(os.Stderr, "linked: the linked parser reads this tree; regenerating once")
	}
	return generate(root)
}

// stages reports whether a run regenerates in two stages: when the linked
// parser cannot parse the tree, because its layout is one behind or it
// refuses a line held (each such file), and -bootstrap asks for it. Stage 1
// itself regenerates once whatever it reads. Without -bootstrap such a
// tree is refused with the instruction.
func stages(layout uint32, held map[string]heldFile, bootstrap, stage1 bool) (bool, error) {
	behind := layout == syntax.PreviousColumnsVersion
	switch {
	case stage1 || !behind && len(held) == 0:
		return false, nil
	case bootstrap:
		return true, nil
	case behind:
		return false, fmt.Errorf("the linked parser writes node layout %d and this tree's compiler/syntax layout %d: a layout change is bootstrapped in two stages: go run ./scripts/linked -bootstrap", layout, syntax.ColumnsVersion)
	}
	paths := slices.Sorted(maps.Keys(held))
	return false, fmt.Errorf("the linked parser refuses lines of %d sources, such as %s (new syntax): regenerate in two stages: go run ./scripts/linked -bootstrap", len(paths), paths[0])
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

// twoStages writes held (what the linked parser refuses, held back), runs
// stage 1 as a fresh build (std is embedded, so the held sources must be
// compiled in), puts the sources back, and runs stage 2 with the parser
// stage 1 wrote.
func twoStages(root string, held map[string]heldFile) error {
	var written []string
	restore := func() {
		for _, path := range written {
			if werr := os.WriteFile(path, []byte(held[path].orig), 0o644); werr != nil {
				fmt.Fprintln(os.Stderr, "linked: put back", path, werr)
			}
		}
	}
	for _, path := range slices.Sorted(maps.Keys(held)) {
		if err := os.WriteFile(path, []byte(held[path].src), 0o644); err != nil {
			restore()
			return err
		}
		written = append(written, path)
		fmt.Fprintln(os.Stderr, "linked: stage 1 holds back lines of", path)
	}
	err := goRun(root, "-stage1")
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

// heldFile is a source with lines the linked parser refuses: its text on
// disk, and the text stage 1 reads, those lines held back.
type heldFile struct{ orig, src string }

// refused comments out, in memory, in the Ergo sources the linked packages
// read (std and compiler, not their testdata), each line the linked parser
// refuses, until every file parses. It answers each such file, by path; a
// tree the linked parser reads answers none. It writes nothing.
func refused(root string) (map[string]heldFile, error) {
	held := map[string]heldFile{}
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
				held[path] = heldFile{orig: orig, src: src}
			}
			return nil
		})
		if err != nil {
			return held, err
		}
	}
	return held, nil
}
