package main

import (
	"fmt"
	"os"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/toolchain"
)

const versionHelp = `usage: ergo version [--json]

Prints the Go release this ergo pins (toolchain.Version) and the go
command its builds run, chosen by ERGO_GO:

  unset or managed  the pinned release, which the first build downloads
                    from GOPROXY as the module golang.org/toolchain,
                    verifies against the checksum database (GOSUMDB) and
                    installs under ergo's cache directory; later builds,
                    offline too, use the cached copy
  system            the go command on PATH
  PATH              that go command

It downloads nothing. --json prints the same facts as one JSON object.
Exit status: 0, or 2 on an invalid invocation.
`

// versionReport is `ergo version --json`.
type versionReport struct {
	// Go is the pinned release.
	Go string `json:"go"`
	// Toolchain is ERGO_GO's choice: managed, system or path.
	Toolchain string `json:"toolchain"`
	// Command is the go command builds run.
	Command string `json:"command"`
	// Installed says whether the managed release is in the cache.
	Installed bool `json:"installed,omitempty"`
}

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
	report := versionReport{Go: toolchain.Version, Toolchain: t.Mode, Command: t.Go, Installed: installed}
	if json {
		return printJSON(report)
	}
	fmt.Printf("ergo prototype, Go %s\n", toolchain.Version)
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
