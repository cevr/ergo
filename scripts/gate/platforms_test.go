package main

import (
	"os"
	"os/exec"
	"testing"
)

// ergoPlatforms are the configurations cmd/ergo must compile in: the
// platforms the gate runs on (Linux on amd64, the workbox; macOS on arm64),
// Linux on arm64, where testinputs observes too, FreeBSD on amd64, which
// builds the files for every other system (`!linux && !darwin`, such as
// replay's stat_other.go), and macOS on amd64, which the release ships
// though no gate runs there.
var ergoPlatforms = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
	{"freebsd", "amd64"},
	{"darwin", "amd64"},
}

// TestErgoCompilesOnEveryPlatform compiles cmd/ergo and every package it
// imports in each of ergoPlatforms, with no link: a file built only off Linux
// (internal/opcount's counter_other.go declared CountSelf twice after a
// merge) breaks the macOS build while the Linux gate stays green. It runs the
// go command because the question is whether the package files Go selects
// for another GOOS compile; go list -export compiles them without linking.
// Mutant: gtd-darwin-countself-twice (internal/opcount ledger).
func TestErgoCompilesOnEveryPlatform(t *testing.T) {
	t.Parallel()
	goCommand := realTools["go"]
	if goCommand == "" {
		t.Fatal("go is required")
	}
	root := checkoutRoot(t)
	for _, c := range ergoPlatforms {
		command := exec.Command(goCommand, "list", "-deps", "-export", "-f", "{{if .Error}}{{.ImportPath}}: {{.Error}}{{end}}", "./cmd/ergo")
		command.Dir = root
		command.Env = setEnvironmentVariable(setEnvironmentVariable(setEnvironmentVariable(os.Environ(), "GOOS", c.goos), "GOARCH", c.goarch), "CGO_ENABLED", "0")
		if out, err := command.CombinedOutput(); err != nil {
			t.Errorf("cmd/ergo does not compile for %s/%s: %v\n%s", c.goos, c.goarch, err, out)
		}
	}
}
