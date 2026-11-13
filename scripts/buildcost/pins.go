package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// pins counts the cold build of a program with and without the pin
// archive sets (CS3; docs/spec/build.md, Pin archives) served from sets,
// a directory of NAME.tar.gz. The clock is post-download: the Go release,
// the patched compile and the sets are on disk; the Ergo cache's store
// and Go's build cache are empty.
//
//	install       the first build, which fetches and installs the sets
//	cold-sets     the store and GOCACHE emptied, the sets kept
//	cold-no-sets  the sets removed too: std and the runtime compile again
func (r runner) pins(label, dir, main, sets string) {
	xdg := filepath.Join(r.work, "xdg-pins-"+label)
	out := filepath.Join(r.work, "out", label)
	pkg := filepath.Join(dir, main)
	url := "ERGO_PIN_URL=file://" + filepath.ToSlash(sets)
	r.countedEnv(label+" install", xdg, []string{url, "GOCACHE=" + filepath.Join(r.work, "gocache-install")}, "build", pkg, "-o", out)
	emptyCaches := func(keepSets bool) {
		entries, _ := os.ReadDir(xdg)
		for _, e := range entries {
			if e.Name() != "ergo" {
				os.RemoveAll(filepath.Join(xdg, e.Name()))
			}
		}
		ergo := filepath.Join(xdg, "ergo")
		entries, _ = os.ReadDir(ergo)
		for _, e := range entries {
			if e.Name() != "toolchain" {
				os.RemoveAll(filepath.Join(ergo, e.Name()))
			}
		}
		if !keepSets {
			os.RemoveAll(filepath.Join(ergo, "toolchain", "pins"))
		}
	}
	emptyCaches(true)
	r.countedEnv(label+" cold-sets", xdg, []string{url, "GOCACHE=" + filepath.Join(r.work, "gocache-sets")}, "build", pkg, "-o", out)
	emptyCaches(false)
	r.countedEnv(label+" cold-no-sets", xdg, []string{"GOCACHE=" + filepath.Join(r.work, "gocache-none")}, "build", pkg, "-o", out)
	fmt.Println()
}
