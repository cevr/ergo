package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// landedStage is the last capstone stage that has landed (S0 to S6 of the
// v1 capstone plan, "Ergo builds Ergo"). Each stage's swap unit raises it,
// and from then on the rows that stage owns must hold.
const landedStage = 0

// capstoneRow is one row of the capstone probe: a fact of the tree that holds
// when the capstone is done, and the stage that makes it hold.
type capstoneRow struct {
	name  string
	stage int
	ok    bool
}

// goCompilerPackages are the Go packages that are the compiler today, each
// with the stage that deletes its last Go product file.
var goCompilerPackages = []struct {
	dir   string
	stage int
}{
	{"internal/sketch/format", 1},
	{"internal/sketch/syntax", 4}, // S2 leaves a generated decoder for the Go checker; S4 deletes it
	{"internal/sketch/check", 4},
	{"internal/sketch/lower", 4},
	{"internal/sketch/machinegraph", 4},
	{"internal/sketch/build", 6},
	{"internal/sketchfront", 6},
	{"internal/gobind", 6},
	{"internal/bindc", 6},
	{"internal/report", 6},
	{"cmd/ergo", 6},
}

// capstoneRows reads the 15 rows of the probe in the tree at root.
func capstoneRows(root string) []capstoneRow {
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, rel))
		return err == nil
	}
	_, lockErr := readLock(filepath.Join(root, lockFile))
	rows := []capstoneRow{
		{"compiler/ergo.mod exists (the compiler is an Ergo module)", 1, exists("compiler/ergo.mod")},
		{"compiler/cmd/ergo/main.ergo exists", 5, exists("compiler/cmd/ergo/main.ergo")},
		{lockFile + " parses (the pinned seed)", 0, lockErr == nil},
		{"scripts/bootstrap exists (stage 1, 2, 3 and the fixpoint check)", 0, exists("scripts/bootstrap")},
	}
	for _, p := range goCompilerPackages {
		n := goProductFiles(filepath.Join(root, p.dir))
		rows = append(rows, capstoneRow{fmt.Sprintf("%s: no Go product files (found %d)", p.dir, n), p.stage, n == 0})
	}
	return rows
}

// goProductFiles counts the .go files that are not tests under dir, outside
// testdata directories.
func goProductFiles(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() && e.Name() == "testdata" {
			return filepath.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			n++
		}
		return nil
	})
	return n
}

// reportCapstone prints the rows and returns how many are red, and the red
// rows of stages that have landed.
func reportCapstone(w io.Writer, rows []capstoneRow) (red int, owed []capstoneRow) {
	for _, r := range rows {
		mark := "ok  "
		if !r.ok {
			mark = "RED "
			red++
			if r.stage <= landedStage {
				owed = append(owed, r)
			}
		}
		fmt.Fprintf(w, "%s S%d %s\n", mark, r.stage, r.name)
	}
	if red == 0 {
		fmt.Fprintln(w, "capstone: done")
	} else {
		fmt.Fprintf(w, "capstone: %d of %d rows red; stages through S%d have landed\n", red, len(rows), landedStage)
	}
	return red, owed
}
