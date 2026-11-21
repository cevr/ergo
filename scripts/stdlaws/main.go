// Command stdlaws writes the std law ledger, internal/sketch/std/ergo.laws
// (runtime.md, Tests 10), from a fresh run of std's rewrite laws
// (sketchfront.FreshStdLedger) and of the fact guards. TestStdLedgerFresh
// fails while the file and a fresh run differ. Run it from the repository
// root.
package main

import (
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/internal/gofacts/hostguard"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

func main() {
	// The guards of the reviewed tables in runtime/v1 run first, and the
	// checker reads their fresh lines (check.FactLedger; LAWFFI LF2d): a
	// shipped ledger with a stale or missing line would otherwise stop std
	// from checking, and this run could never refresh it. The shipped line
	// of each fresh key is dropped, so the checker reads the fresh one.
	host := hostguard.Entries(toolchain.Version)
	fresh := map[string]bool{}
	var lines []string
	for _, e := range host {
		fresh["fact "+e.Key+" "] = true
		lines = append(lines, e.Line())
	}
	var kept []string
	for _, l := range strings.Split(stdlib.Ledger(), "\n") {
		stale := false
		for k := range fresh {
			stale = stale || strings.HasPrefix(l, k)
		}
		if !stale {
			kept = append(kept, l)
		}
	}
	ledger := strings.Join(append(kept, lines...), "\n") + "\n"
	check.FactLedger = func() string { return ledger }

	root, err := build.DefaultRoot()
	if err == nil {
		var text string
		// The check runs of std's laws at unchanged inputs are cached
		// (lawcache), so a second run builds and runs nothing.
		var store *lawcache.Store
		if compiler, cerr := build.CompilerIdentity(); cerr == nil {
			store = lawcache.Open(root, compiler)
		}
		text, err = sketchfront.FreshStdLedger(sketchfront.BuildRequest{Cache: build.Cache{Root: root}, Toolchain: build.HostToolchain(), Stderr: os.Stderr, Laws: store}, host)
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
