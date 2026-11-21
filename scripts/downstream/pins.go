package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// pinsFile and movedFile are the downstream check's files, from the
// repository root (docs/tooling.md, Downstream check).
const (
	pinsFile  = "downstream/pins"
	movedFile = "downstream/moved"
)

// pin is one line of downstream/pins: a repository ergo must not break,
// at one commit.
type pin struct {
	name, url, commit string
}

// commitPattern is a full commit hash. A pin is a commit, never a branch
// or a tag, so a downstream repository's push cannot change what ergo's
// gate builds (models/downstream: FloatingPin.NeverArrives).
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// namePattern is a pin's name: the cache folder's name and the word that
// selects it.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// parsePins reads downstream/pins: one `NAME URL COMMIT` line per
// repository; a blank line and a line starting with # are ignored.
func parsePins(text string) ([]pin, error) {
	var pins []pin
	seen := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s:%d: want NAME URL COMMIT, got %q", pinsFile, i+1, line)
		}
		p := pin{name: fields[0], url: fields[1], commit: fields[2]}
		switch {
		case !namePattern.MatchString(p.name):
			return nil, fmt.Errorf("%s:%d: %q is not a pin name (lower-case letters, digits, '.', '_' and '-')", pinsFile, i+1, p.name)
		case seen[p.name]:
			return nil, fmt.Errorf("%s:%d: %s is pinned twice", pinsFile, i+1, p.name)
		case !commitPattern.MatchString(p.commit):
			return nil, fmt.Errorf("%s:%d: %q is not a full commit hash; a pin is a commit, never a branch or a tag", pinsFile, i+1, p.commit)
		}
		seen[p.name] = true
		pins = append(pins, p)
	}
	return pins, nil
}

// readPins reads the pins file; a missing file is an error.
func readPins(path string) ([]pin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parsePins(string(data))
}

// movedPaths counts the moved-path lines of downstream/moved (a missing
// file holds none). The lines themselves are TestMovedPathsAreFrozen's
// (internal/residue).
func movedPaths(path string) (int, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n, nil
}

// selectPins returns the pins names selects, in the file's order; no name
// selects every pin. An unknown name is an error.
func selectPins(pins []pin, names []string) ([]pin, error) {
	if len(names) == 0 {
		return pins, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []pin
	for _, p := range pins {
		if want[p.name] {
			out = append(out, p)
			delete(want, p.name)
		}
	}
	for _, n := range names {
		if want[n] {
			return nil, fmt.Errorf("no pin named %s in %s", n, pinsFile)
		}
	}
	return out, nil
}
