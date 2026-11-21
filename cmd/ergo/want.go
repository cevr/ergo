package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
)

// wantMarker is a fixture's `// want CODE` at the end of a line, or
// `// want CODE CODE` for two diagnostics on that line.
// A quoted text after the codes, a Go string literal, must be a part of a
// message on that line: `// want S102 "requires B,"`.
var wantMarker = regexp.MustCompile(`// want ([A-Z]+\d{3}(?: [A-Z]+\d{3})*)(?: ("(?:[^"\\]|\\.)*"))?\s*$`)

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
		diags := sketchfront.Check(string(data)).Diagnostics
		want, have := wantedCodes(string(data)), checkedCodes(diags)
		if !slices.Equal(want, have) {
			differ++
			fmt.Fprintf(w, "%s: want [%s], got [%s]\n", file, strings.Join(want, " "), strings.Join(have, " "))
			continue
		}
		if why := wantedTexts(string(data), diags); why != "" {
			differ++
			fmt.Fprintf(w, "%s: %s\n", file, why)
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

// checkedCodes is diagnostics as CODE@LINE, sorted; a diagnostic with no
// line is CODE@0, which no marker wants, and a code the checker does not
// register is UNREGISTERED:CODE and one with a negative length is
// NEGATIVE-LENGTH:CODE, which no marker wants either.
func checkedCodes(diags []report.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		code := d.Code
		if strings.HasPrefix(code, "S") && !check.Registered(code) {
			code = "UNREGISTERED:" + code
		}
		if d.Span.Length < 0 {
			code = "NEGATIVE-LENGTH:" + code
		}
		out = append(out, fmt.Sprintf("%s@%d", code, d.Span.Line))
	}
	slices.Sort(out)
	return out
}

// wantedTexts is "" when each marker's quoted text (`// want CODE
// "text"`, a Go string literal) is a part of a message on its line, else
// the first one that is not.
func wantedTexts(src string, diags []report.Diagnostic) string {
	for i, line := range strings.Split(src, "\n") {
		m := wantMarker.FindStringSubmatch(line)
		if m == nil || m[2] == "" {
			continue
		}
		text, err := strconv.Unquote(m[2])
		if err != nil {
			return fmt.Sprintf("line %d: the marker's text %s is not a Go string", i+1, m[2])
		}
		found := false
		for _, d := range diags {
			found = found || d.Span.Line == i+1 && strings.Contains(d.Message, text)
		}
		if !found {
			return fmt.Sprintf("line %d: no message there holds %q", i+1, text)
		}
	}
	return ""
}
