package main

import (
	"fmt"
	"os"

	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/toolchain"
)

const versionHelp = `usage: ergo version [--json]

Prints this ergo's own version, then the Go release it pins
(toolchain.Version) and the go command its builds run.

The version is the Go module version the go command stamped into the
binary: a tag (v1.2.3) when installed with go install …@v1.2.3, else the
pseudo-version of the commit it was built from
(v0.0.0-20261117152120-c501620891c0, +dirty for a modified tree), else
devel (a build with -buildvcs=false, or outside a repository). A module's
ergo.mod line "ergo VERSION" names the lowest ergo that may build it; an
older ergo refuses the module, and a devel ergo accepts every line.

The go command is chosen by ERGO_GO:

  unset or managed  the pinned release, which the first build downloads
                    from GOPROXY as the module golang.org/toolchain,
                    verifies against the checksum database (GOSUMDB) and
                    installs under ergo's cache directory; later builds,
                    offline too, use the cached copy
  system            the go command on PATH
  PATH              that go command

It downloads nothing. --json prints the same facts as one JSON object,
with "features": what this ergo has that a tool driving it may require
(versionFeatures), since a version orders builds by time, and a build of
another branch may be newer and lack them.
Exit status: 0, or 2 on an invalid invocation.
`

// versionReport is `ergo version --json`.
type versionReport struct {
	// Version is this ergo's own version (internal/ergoversion): a tag, a
	// pseudo-version, or devel for a build with no version stamp.
	Version string `json:"version"`
	// Go is the pinned release.
	Go string `json:"go"`
	// Toolchain is ERGO_GO's choice: managed, system or path.
	Toolchain string `json:"toolchain"`
	// Command is the go command builds run.
	Command string `json:"command"`
	// Installed says whether the managed release is in the cache.
	Installed bool `json:"installed,omitempty"`
	// Features is versionFeatures.
	Features []string `json:"features"`
}

// versionFeatures are the abilities a tool that drives ergo checks for
// (orrery, MODELCHECK T3d O1), by name, never removed once listed:
//
//	check-strict       check --strict exits 1 on a law it did not decide
//	machine-laws       check searches a machine's laws by predicate
//	conform            ergo conform DIR --machine M --trace FILE
//	conform-exit-codes conform exits 0, 20 mismatch, 21 trace error,
//	                   22 over the limit, 23 a law broken (everystate's)
//	version-features   this list
var versionFeatures = []string{"check-strict", "machine-laws", "conform", "conform-exit-codes", "version-features"}

func versionCommand(args []string) error {
	json := false
	for _, arg := range args {
		if arg != "--json" {
			return usageError{message: "usage: ergo version [--json]"}
		}
		json = true
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	t, installed := toolchain.Status(os.Getenv, root)
	report := versionReport{Version: ergoversion.Current(), Go: toolchain.Version, Toolchain: t.Mode, Command: t.Go, Installed: installed, Features: versionFeatures}
	if json {
		return printJSON(report)
	}
	fmt.Printf("ergo %s, Go %s\n", report.Version, toolchain.Version)
	switch {
	case t.Mode == "managed" && installed:
		fmt.Printf("toolchain: managed, %s\n", t.Root)
	case t.Mode == "managed":
		fmt.Printf("toolchain: managed, not yet downloaded (into %s)\n", t.Root)
	case t.Mode == "system":
		fmt.Println("toolchain: system, the go command on PATH (ERGO_GO=system)")
	default:
		fmt.Printf("toolchain: %s (ERGO_GO)\n", t.Go)
	}
	return nil
}
