package main

import (
	"os"
	"strings"

	"ergo.local/prototype/internal/sketch/lawfix"
)

const fixLawsHelp = `
Then it moves each top-level law that does not sit in a file of its
subjects (S115; types.md, Laws 15) beside its main subject, with each law
helper beside the first law or test that uses it and each provider the
laws run against beside their operation type; a file of laws left holding
other declarations is named by its concept (internal/sketch/lawfix). Here a
PATH is a package folder, or DIR/... for every package of its module below
DIR, and --std DIR alone (no PATH) moves the laws of the standard library
in DIR. It prints each move, each file it deleted (no declaration left) and
each law that names nothing in its package. --check does not run this pass
while S115 is off.
`

// fixLaws is ergo fix's law pass (lawfix): it moves the laws under paths,
// and the standard library's in stdRoot when it is not empty, and returns
// the report's text.
func fixLaws(paths []string, stdRoot string, write bool) (string, error) {
	// A law moves within its package: the pass reads package folders and
	// DIR/..., never a single file.
	var dirs []string
	for _, p := range paths {
		if info, err := os.Stat(p); p == "..." || strings.HasSuffix(p, "/...") || err == nil && info.IsDir() {
			dirs = append(dirs, p)
		}
	}
	if len(dirs) == 0 && stdRoot == "" {
		return "", nil
	}
	rep, err := lawfix.Run(lawfix.Options{Paths: dirs, StdRoot: stdRoot, Write: write})
	if err != nil {
		return "", err
	}
	return rep.Text(), nil
}

// fixFolders is paths with a DIR/... written as DIR: the other passes walk
// a folder already.
func fixFolders(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		if p == "..." {
			p = "."
		}
		out[i] = strings.TrimSuffix(p, "/...")
	}
	return out
}
