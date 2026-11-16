// Command stdlaws writes the std law ledger, internal/sketch/std/ergo.laws
// (runtime.md, Tests 10), from a fresh run of std's rewrite laws
// (sketchfront.FreshStdLedger). TestStdLedgerFresh fails while the file
// and a fresh run differ. Run it from the repository root.
package main

import (
	"fmt"
	"os"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketchfront"
)

func main() {
	root, err := build.DefaultRoot()
	if err == nil {
		var text string
		// The check runs of std's laws at unchanged inputs are cached
		// (lawcache), so a second run builds and runs nothing.
		var store *lawcache.Store
		if compiler, cerr := build.CompilerIdentity(); cerr == nil {
			store = lawcache.Open(root, compiler)
		}
		text, err = sketchfront.FreshStdLedger(sketchfront.BuildRequest{Cache: build.Cache{Root: root}, Toolchain: build.HostToolchain(), Stderr: os.Stderr, Laws: store})
		build.Cache{Root: root}.LawsWritten(store.Wrote())
		if err == nil {
			err = os.WriteFile("internal/sketch/std/ergo.laws", []byte(text), 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stdlaws:", err)
		os.Exit(1)
	}
}
