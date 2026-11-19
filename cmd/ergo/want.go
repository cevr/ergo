package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketchfront"
)

// wantMarker is a fixture's `// want CODE` at the end of a line, or
// `// want CODE CODE` for two diagnostics on that line.
var wantMarker = regexp.MustCompile(`// want ([A-Z]+\d{3}(?: [A-Z]+\d{3})*)\s*$`)

// checkWant is `ergo check --want PATH...` (tooling.md, check): each
// PATH is a fixture file or a folder of them (its *.ergo files, not its
// subfolders), and each fixture is checked alone, as one source, as
// `ergo check FILE` checks it. A fixture passes when its diagnostics are
// exactly its `// want` markers: each code on its marked line, as many
// times as marked, and no diagnostic on any other line. It prints a line
// per fixture that differs, with what it wanted and what it got, then
// the count; a fixture that differs is exit 1. No evaluation runs: a
// fixture is about the checker's diagnostics.
func checkWant(w io.Writer, paths []string) error {
	if len(paths) == 0 {
		return invalidInvocation(fmt.Errorf("--want takes a fixture file or folder"))
	}
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		found, err := filepath.Glob(filepath.Join(p, "*.ergo"))
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return fmt.Errorf("%s holds no .ergo fixture", p)
		}
		files = append(files, found...)
	}
	differ := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		want, have := wantedCodes(string(data)), checkedCodes(string(data))
		if !slices.Equal(want, have) {
			differ++
			fmt.Fprintf(w, "%s: want [%s], got [%s]\n", file, strings.Join(want, " "), strings.Join(have, " "))
		}
	}
	if differ > 0 {
		fmt.Fprintf(w, "%d of %d fixtures differ\n", differ, len(files))
		return fmt.Errorf("%d of %d fixtures differ from their // want markers", differ, len(files))
	}
	fmt.Fprintf(w, "ok %d fixtures\n", len(files))
	return nil
}

// wantedCodes is a source's `// want` markers as CODE@LINE, one per code,
// sorted.
func wantedCodes(src string) []string {
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if m := wantMarker.FindStringSubmatch(line); m != nil {
			for _, code := range strings.Fields(m[1]) {
				out = append(out, fmt.Sprintf("%s@%d", code, i+1))
			}
		}
	}
	slices.Sort(out)
	return out
}

// checkedCodes is the diagnostics of checking src alone as CODE@LINE,
// sorted; a diagnostic with no line is CODE@0, which no marker wants.
func checkedCodes(src string) []string {
	var out []string
	for _, d := range sketchfront.Check(src).Diagnostics {
		out = append(out, fmt.Sprintf("%s@%d", d.Code, d.Span.Line))
	}
	slices.Sort(out)
	return out
}
