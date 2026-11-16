// Command vetgoos checks every GOOS the Go toolchain lists (`go tool dist
// list`), one architecture each (amd64 where the GOOS has it), so that no
// system is a silent gap (FU-freebsd: GOOS=freebsd go vet failed in
// runtime/v1). Each GOOS is in exactly one class:
//
//   - vetted: `go vet ./...` with that GOOS and cgo off must pass;
//   - unsupported by the runtime: `go build -gcflags=-e ./runtime/v1` must
//     fail with the build-tag diagnostic of runtime/v1/unsupported_os.go,
//     which names the reason;
//   - external linking only: the toolchain links the GOOS only with cgo,
//     which Ergo turns off (docs/spec/interop.md, Platforms); `go vet
//     ./runtime/v1` must fail with the toolchain's own message.
//
// A GOOS in no class fails the run: classify it here. Usage: go run
// ./scripts/vetgoos [GOOS...] (default: every GOOS). Exit 0 when every
// GOOS checked holds, 1 when one does not, 2 for a usage error.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// vetted are the systems the runtime runs on, whose whole module vets.
var vetted = []string{"darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd"}

// unsupported are the systems the runtime refuses by its build tag.
var unsupported = []string{"aix", "illumos", "js", "plan9", "solaris", "wasip1", "windows"}

// externalOnly are the systems Go links only with cgo.
var externalOnly = []string{"android", "ios"}

// diagnostic is the name runtime/v1/unsupported_os.go leaves undefined.
const diagnostic = "ergoRuntimeNeedsLinuxDarwinOrBSD"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	list, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vetgoos: go tool dist list: %v\n", err)
		return 2
	}
	arch := map[string]string{}
	var systems []string
	for _, line := range strings.Fields(string(list)) {
		goos, goarch, ok := strings.Cut(line, "/")
		if !ok {
			continue
		}
		if _, seen := arch[goos]; !seen {
			systems = append(systems, goos)
			arch[goos] = goarch
		}
		if goarch == "amd64" {
			arch[goos] = goarch
		}
	}
	if len(args) > 0 {
		for _, goos := range args {
			if _, ok := arch[goos]; !ok {
				fmt.Fprintf(os.Stderr, "vetgoos: the toolchain lists no GOOS %q\n", goos)
				return 2
			}
		}
		systems = args
	}
	failed := false
	for _, goos := range systems {
		if problem := check(goos, arch[goos]); problem != "" {
			failed = true
			fmt.Printf("FAIL %s/%s: %s\n", goos, arch[goos], problem)
		} else {
			fmt.Printf("ok %s/%s\n", goos, arch[goos])
		}
	}
	if failed {
		return 1
	}
	return 0
}

// check runs goos's class check and returns what went wrong, or "".
func check(goos, goarch string) string {
	switch {
	case slices.Contains(vetted, goos):
		if out, err := gocmd(goos, goarch, "vet", "./..."); err != nil {
			return "go vet ./... failed:\n" + indent(out)
		}
	case slices.Contains(unsupported, goos):
		out, err := gocmd(goos, goarch, "build", "-gcflags=-e", "./runtime/v1")
		if err == nil || !strings.Contains(out, diagnostic) {
			return "runtime/v1 does not fail with its build-tag diagnostic " + diagnostic + ":\n" + indent(out)
		}
	case slices.Contains(externalOnly, goos):
		out, err := gocmd(goos, goarch, "vet", "./runtime/v1")
		if err == nil || !strings.Contains(out, "requires external (cgo) linking") {
			return "the toolchain no longer requires cgo here; move it to vetted or unsupported:\n" + indent(out)
		}
	default:
		return "no class: add it to vetted, unsupported or externalOnly in scripts/vetgoos"
	}
	return ""
}

// gocmd runs the go command for goos/goarch with cgo off.
func gocmd(goos, goarch string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 12 {
		lines = append(lines[:12], "...")
	}
	return "    " + strings.Join(lines, "\n    ")
}
