package main

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// compilerFile is the linked compiler's Go, whose parser ParseRows runs.
const compilerFile = "internal/linked/compiler/compiler.go"

// walkBound stops a runaway walk: past this many probes the bootstrap
// fails, naming each parser tried and its first refusal. It is no
// heuristic: the walk ends at the first parser that refuses nothing.
const walkBound = 16

// refusal is a line a linked parser refuses: its file, relative to the
// repository root with slashes, and its text.
type refusal struct{ path, text string }

func (r refusal) String() string { return fmt.Sprintf("%s: %q", r.path, r.text) }

// probed is a linked parser's answer to -probe: the layout it writes and
// the lines of the tree it refuses, in path order (refused -1 when it
// writes a layout the decoder does not read, or the probe failed).
type probed struct {
	from    string
	layout  uint32
	refused int
	lines   []refusal
}

// readable reports whether p writes a layout the decoder reads.
func (p probed) readable() bool {
	return p.refused >= 0 && (p.layout == syntax.ColumnsVersion || p.layout == syntax.PreviousColumnsVersion)
}

// clean reports whether p reads the whole tree.
func (p probed) clean() bool { return p.readable() && p.refused == 0 }

func (p probed) first() string {
	switch {
	case p.refused < 0:
		return fmt.Sprintf("layout %d, which the decoder does not read", p.layout)
	case len(p.lines) == 0:
		return "refuses nothing"
	}
	return fmt.Sprintf("refuses %d lines, first %s", p.refused, p.lines[0])
}

// candidate is a committed linked parser: the commit that wrote it, its
// Go, and whether the commit's tree holds the disk parser's first refused
// line (the filter: a parser whose tree never held the construct cannot
// have been written to read it).
type candidate struct {
	commit string
	src    []byte
	holds  bool
}

// choice is the parser stage 1 regenerates with: cand's index, or -1 for
// the disk's; held when it still refuses lines stage 1 holds back. note
// is the result line's reason, tried each parser probed.
type choice struct {
	cand  int
	held  bool
	note  string
	tried []probed
}

// plan picks stage 1's parser: the disk's when it reads the tree; else the
// newest committed parser whose tree holds the disk's first refusal and
// that refuses nothing; else, when no commit holds that line (syntax newer
// than every linked parser: the tree's own port), the disk's, its refused
// lines held back. A walk that probes walkBound parsers, or ends with none
// that reads the tree, fails naming each one tried: never a silent
// fallback.
func plan(disk probed, diskFrom string, cands []candidate, probe func(candidate) probed) (choice, error) {
	if disk.clean() {
		return choice{cand: -1, note: "the disk parser " + diskFrom + " reads the tree"}, nil
	}
	var tried []probed
	filtered := false
	for i, c := range cands {
		if !c.holds {
			continue
		}
		filtered = true
		if len(tried) == walkBound {
			return choice{}, walkError(fmt.Sprintf("the walk probed %d parsers (the runaway bound)", walkBound), disk, tried)
		}
		p := probe(c)
		tried = append(tried, p)
		if p.clean() {
			return choice{cand: i, note: "the parser of " + c.commit + " reads the tree", tried: tried}, nil
		}
	}
	if !filtered && disk.readable() && len(disk.lines) > 0 {
		return choice{cand: -1, held: true, tried: tried, note: fmt.Sprintf("held back %d lines with the disk parser %s: no committed parser reads %q (no commit that wrote %s holds that line of %s)", disk.refused, diskFrom, disk.lines[0].text, compilerFile, disk.lines[0].path)}, nil
	}
	return choice{}, walkError(fmt.Sprintf("no parser of the %d commits that wrote %s and hold the refused line reads the tree", len(tried), compilerFile), disk, tried)
}

func walkError(why string, disk probed, tried []probed) error {
	var b strings.Builder
	fmt.Fprintf(&b, "stage 1: %s; the disk parser %s", why, disk.first())
	for _, p := range tried {
		fmt.Fprintf(&b, "\n  %s: %s", p.from, p.first())
	}
	return fmt.Errorf("%s", b.String())
}

// finish is the bootstrap's last check: the parser the last stage wrote
// reads the whole tree at this tree's layout. Lines held back are a
// stage-1 state, never a result.
func finish(p probed) error {
	if p.clean() && p.layout == syntax.ColumnsVersion {
		return nil
	}
	if p.refused < 0 || p.layout != syntax.ColumnsVersion {
		return fmt.Errorf("after the last stage the linked parser writes layout %d, not %d", p.layout, syntax.ColumnsVersion)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "after the last stage the linked parser still refuses %d lines:", p.refused)
	for _, l := range p.lines {
		fmt.Fprintf(&b, "\n  %s", l)
	}
	return fmt.Errorf("%s", b.String())
}

// history is the committed linked parsers, newest first across all
// parents of HEAD (and MERGE_HEAD while a merge is uncommitted): each
// commit that wrote compilerFile, its Go unless an earlier candidate or the
// disk has the same, and whether its tree holds line (every commit holds
// it when line is empty: the disk parser writes a layout the decoder does
// not read, so it names no refusal).
func history(root string, disk []byte, line refusal) ([]candidate, error) {
	revs := []string{"HEAD"}
	if exec.Command("git", "--no-optional-locks", "-C", root, "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil {
		revs = append(revs, "MERGE_HEAD")
	}
	out, err := exec.Command("git", append(append([]string{"--no-optional-locks", "-C", root, "log", "--full-history", "--format=%h"}, revs...), "--", compilerFile)...).Output()
	if err != nil {
		return nil, fmt.Errorf("git log -- %s: %w", compilerFile, err)
	}
	seen := [][]byte{disk}
	var cands []candidate
	for _, h := range strings.Fields(string(out)) {
		src, err := exec.Command("git", "--no-optional-locks", "-C", root, "show", h+":"+compilerFile).Output()
		if err != nil || slices.ContainsFunc(seen, func(b []byte) bool { return bytes.Equal(b, src) }) {
			continue
		}
		seen = append(seen, src)
		holds := true
		if line.path != "" {
			file, err := exec.Command("git", "--no-optional-locks", "-C", root, "show", h+":"+line.path).Output()
			holds = err == nil && holdsLine(string(file), line.text)
		}
		cands = append(cands, candidate{commit: h, src: src, holds: holds})
	}
	return cands, nil
}

// holdsLine reports whether src has a line of text.
func holdsLine(src, text string) bool {
	return slices.Contains(strings.Split(src, "\n"), text)
}

// heldLines is the lines held refuses, in path order, paths relative to
// root.
func heldLines(root string, held map[string]heldFile) []refusal {
	var out []refusal
	for _, path := range slices.Sorted(maps.Keys(held)) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		orig, src := strings.Split(held[path].orig, "\n"), strings.Split(held[path].src, "\n")
		for i := range min(len(orig), len(src)) {
			if strings.HasPrefix(src[i], heldMark) && !strings.HasPrefix(orig[i], heldMark) {
				out = append(out, refusal{path: filepath.ToSlash(rel), text: orig[i]})
			}
		}
	}
	return out
}

// printProbe is -probe's answer: "layout L refused N", then
// "refuses PATH<TAB>TEXT" per line.
func printProbe(layout uint32, lines []refusal, refusedN int) {
	fmt.Printf("layout %d refused %d\n", layout, refusedN)
	for _, l := range lines {
		fmt.Printf("refuses %s\t%s\n", l.path, l.text)
	}
}

// readProbe reads printProbe's answer.
func readProbe(from string, out string) probed {
	p := probed{from: from, refused: -1}
	ls := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if _, err := fmt.Sscanf(ls[0], "layout %d refused %d", &p.layout, &p.refused); err != nil {
		p.refused = -1
		return p
	}
	for _, l := range ls[1:] {
		if rest, ok := strings.CutPrefix(l, "refuses "); ok {
			path, text, _ := strings.Cut(rest, "\t")
			p.lines = append(p.lines, refusal{path: path, text: text})
		}
	}
	return p
}

// twoStages bootstraps: it probes the disk parser, picks stage 1's parser
// (plan, over history), puts it on disk, holds back what it still refuses
// (-hold), runs stage 1 as a fresh build (std is embedded, so the held
// sources are compiled in), puts the sources back, runs stage 2 with the
// parser stage 1 wrote, and checks that parser reads the tree (finish).
// The result line names the parser stage 1 used. A failed stage 1 puts
// the compiler on disk back.
func twoStages(root string) error {
	path := filepath.Join(root, filepath.FromSlash(compilerFile))
	disk, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	diskFrom := "worktree"
	if head, err := exec.Command("git", "--no-optional-locks", "-C", root, "show", "HEAD:"+compilerFile).Output(); err == nil && bytes.Equal(head, disk) {
		if h, err := exec.Command("git", "--no-optional-locks", "-C", root, "log", "-1", "--format=%h", "--", compilerFile).Output(); err == nil {
			diskFrom = strings.TrimSpace(string(h))
		}
	}
	putBack := func() {
		if err := os.WriteFile(path, disk, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "linked: put back", path, err)
		}
	}
	diskP := probeWith(root, path, diskFrom, disk, "-probe")
	var first refusal
	if diskP.readable() && len(diskP.lines) > 0 {
		first = diskP.lines[0]
	}
	cands, err := history(root, disk, first)
	if err != nil {
		return err
	}
	c, err := plan(diskP, diskFrom, cands, func(c candidate) probed {
		p := probeWith(root, path, c.commit, c.src, "-probe")
		fmt.Fprintf(os.Stderr, "linked: the parser of %s: %s\n", c.commit, p.first())
		return p
	})
	if err != nil {
		putBack()
		return err
	}
	src, from := disk, diskFrom
	if c.cand >= 0 {
		src, from = cands[c.cand].src, cands[c.cand].commit
	}
	fmt.Fprintf(os.Stderr, "linked: stage 1: %s\n", c.note)
	orig, err := sources(root)
	if err != nil {
		putBack()
		return err
	}
	restore := func() {
		now, err := sources(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "linked: put back the held sources:", err)
			return
		}
		for p, s := range orig {
			if now[p] != s {
				if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, "linked: put back", p, err)
				}
			}
		}
	}
	if p := probeWith(root, path, from, src, "-hold"); p.refused < 0 {
		restore()
		putBack()
		return fmt.Errorf("stage 1: the parser of %s did not hold back what it refuses", from)
	}
	err = goRun(root, "-stage1")
	restore()
	if err != nil {
		putBack()
		return fmt.Errorf("stage 1 (the parser of %s): %w", from, err)
	}
	if err := goRun(root); err != nil {
		return fmt.Errorf("stage 2: %w", err)
	}
	now, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := finish(probeWith(root, path, "stage 2", now, "-probe")); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "linked: bootstrapped: stage 1 used the parser of %s (%s); the parser stage 2 wrote reads the tree\n", from, c.note)
	return nil
}

// probeWith runs scripts/linked with flag (-probe or -hold) with src as
// the linked compiler on disk, and reads its answer.
func probeWith(root, path, from string, src []byte, flag string) probed {
	if err := os.WriteFile(path, src, 0o644); err != nil {
		return probed{from: from, refused: -1}
	}
	cmd := exec.Command("go", "run", "./scripts/linked", flag)
	cmd.Dir, cmd.Stderr = root, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return probed{from: from, refused: -1}
	}
	return readProbe(from, string(out))
}
