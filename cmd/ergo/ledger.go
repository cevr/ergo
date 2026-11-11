package main

import (
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketch/lower"
)

// ledgerPath is the law ledger of the program at path (runtime.md, Tests
// 10): the module-wide ergo.laws beside the nearest ergo.mod at or above the program's
// folder (the file's folder for a file), else in that folder.
func ledgerPath(path string) string {
	dir := path
	if !isPackageFolder(path) {
		dir = filepath.Dir(path)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for d := dir; ; d = filepath.Dir(d) {
		if info, err := os.Stat(filepath.Join(d, "ergo.mod")); err == nil && info.Mode().IsRegular() {
			return filepath.Join(d, lower.LedgerFile)
		}
		if filepath.Dir(d) == d {
			return filepath.Join(dir, lower.LedgerFile)
		}
	}
}
