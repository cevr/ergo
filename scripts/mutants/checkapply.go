package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ergo.local/prototype/scripts/mutants/anchor"
)

// checkApply is -check-apply (MN0; the rule LEDGER ROWS TRAVEL WITH THE
// CODE): it judges nothing and only locates each selected row's edit, as
// the runner and TestLedgerEditsApplyOnce do (anchor.Locate). For each row
// that does not apply it prints one line: the ledger, the row, its owner
// (origin), why, and the last commit that changed how often its from occurs
// in its file (git log -S), which is the commit that rewrote the code and
// owes the row. It exits 1 when a row does not apply.
func checkApply(root string, ledgers []string, sel func(string) bool, stdout, stderr io.Writer) int {
	bad := 0
	for _, path := range ledgers {
		l, err := readLedger(root, path, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range l.Mutants {
			if !sel(m.Name) {
				continue
			}
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m.File)))
			if err == nil {
				_, err = anchor.Locate(m.File, src, m.Decl, m.From)
			}
			if err == nil {
				continue
			}
			bad++
			fmt.Fprintf(stdout, "%s\t%s\towner %s\t%v\tlast changed by %s\n", rel, m.Name, m.Origin, err, lastChange(root, m.File, m.From))
		}
	}
	fmt.Fprintf(stdout, "check-apply: %d rows do not apply\n", bad)
	if bad > 0 {
		return 1
	}
	return 0
}

// lastChange names the newest commit that changed how often from occurs in
// file (git's pickaxe), as "HASH subject", or why there is none.
func lastChange(root, file, from string) string {
	out, err := exec.Command("git", "--no-optional-locks", "-C", root, "log", "-1", "--format=%h %s", "-S", from, "--", file).Output()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		return s
	}
	return "no commit in this history (the file moved, or from never occurred)"
}
