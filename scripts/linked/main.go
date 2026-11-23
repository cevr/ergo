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
// as a new token). Stage 1 regenerates with a parser that reads the tree:
// the disk's when it does; else the newest committed one that does, of
// the commits that wrote the linked compiler (across every parent, so a
// syntax port merged several merges back is found) whose tree holds the
// disk parser's first refused line. A line held back can take a
// declaration std needs with it (a prelude type set held back leaves std
// cmp without ordered), so a committed parser that reads the syntax is
// always preferred. Only when no such commit holds the line (the tree's
// own syntax change, newer than every linked parser) does stage 1 use the
// disk's parser with the lines it refuses held back: commented out on
// disk, and put back when stage 1 ends. A walk that finds no parser fails,
// naming each it tried (walk.go). Stage 2 then regenerates with the
// parser stage 1 wrote, which must read the whole tree. -bootstrap on a tree the
// linked parser reads regenerates once. Without -bootstrap, a tree that
// needs two stages is refused with this instruction: never decoded at the
// wrong width, never regenerated without its new syntax.
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
	probe     = flag.Bool("probe", false, "print the linked parser's layout and the lines of this tree it refuses, and regenerate nothing")
	hold      = flag.Bool("hold", false, "-probe, and hold back on disk the lines the linked parser refuses (the step before stage 1)")
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
	if *probe || *hold {
		if layout != syntax.ColumnsVersion && !behind {
			printProbe(layout, nil, -1)
			return nil
		}
		held, err := refused(root)
		if err != nil {
			return err
		}
		if *hold {
			for _, path := range slices.Sorted(maps.Keys(held)) {
				if err := os.WriteFile(path, []byte(held[path].src), 0o644); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, "linked: stage 1 holds back lines of", path)
			}
		}
		printProbe(layout, heldLines(root, held), refusedLines(held))
		return nil
	}
	if layout != syntax.ColumnsVersion && !behind && !*bootstrap {
		return &syntax.LayoutError{Linked: layout}
	}
	if *stage1 {
		return generate(root) // the held-back sources are on disk and embedded: twoStages
	}
	if layout != syntax.ColumnsVersion && !behind {
		return twoStages(root) // a layout the decoder does not read: only a parent's parser can serve
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
		return twoStages(root)
	}
	if *bootstrap {
		fmt.Fprintln(os.Stderr, "linked: the linked parser reads this tree; regenerating once")
	}
	return generate(root)
}

// refusedLines is the number of lines held back in held.
func refusedLines(held map[string]heldFile) int {
	n := 0
	for _, h := range held {
		n += strings.Count(h.src, heldMark) - strings.Count(h.orig, heldMark)
	}
	return n
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

// sources are the Ergo sources a hold-back may change, by path: std's and
// the compiler's, not their testdata.
func sources(root string) (map[string]string, error) {
	out := map[string]string{}
	for _, dir := range []string{"internal/sketch/std", "compiler"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && e.Name() == "testdata" {
				return filepath.SkipDir
			}
			if e.IsDir() || !strings.HasSuffix(path, ".ergo") {
				return nil
			}
			b, err := os.ReadFile(path)
			out[path] = string(b)
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
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
