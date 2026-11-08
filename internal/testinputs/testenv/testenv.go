// Package testenv names the environment a cached Go test may receive.
//
// Observation (package testinputs) keys a variable only if a process
// received it or the test process looked it up. A program run by a test that
// reads a variable absent when the result was cached leaves no trace, since
// its lookup is a library call, not a system call, so setting the variable
// later replays the cached result. cmd/go's test log cannot say "every
// variable, present or not". A runner closes the gap by passing a cached
// test only the variables named here, as Bazel's strict action environment
// and Nix's pure builds do, and by keying their values (the gate puts them in
// ToolDigest). Any other variable cannot reach the test, so it cannot be an
// unkeyed input.
package testenv

import (
	"slices"
	"strings"
)

// ToolDigest is the variable through which the gate passes a digest of the
// test tools (git, sh) and the values of the variables in Names. cmd/go rechecks only files inside the
// module root, so it stands for the programs and libraries outside the root
// that observed processes run, and for variables they read.
const ToolDigest = "ERGO_TOOL_VERSIONS"

// Names are the variables a cached Go test may receive.
var Names = []string{
	// Where programs, the home directory and temporary files are.
	"PATH", "HOME", "TMPDIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
	// The go command's settings (go help environment), which go builds and
	// go runs inside tests read, and which select its caches.
	"AR", "CC", "CXX", "GCCGO", "PKG_CONFIG",
	"CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_ENABLED", "CGO_FFLAGS", "CGO_LDFLAGS",
	"GO111MODULE", "GOAMD64", "GOARCH", "GOARM64", "GOAUTH", "GOBIN", "GOCACHE", "GOCACHEPROG",
	"GOENV", "GOEXPERIMENT", "GOFIPS140", "GOFLAGS", "GOINSECURE", "GOMODCACHE", "GONOPROXY",
	"GONOSUMDB", "GOOS", "GOPATH", "GOPRIVATE", "GOPROXY", "GOROOT", "GOSUMDB", "GOTELEMETRY",
	"GOTELEMETRYDIR", "GOTMPDIR", "GOTOOLCHAIN", "GOVCS", "GOWORK",
	// The Go runtime's, which every Go program reads at startup.
	"GODEBUG", "GOGC", "GOMAXPROCS", "GOMEMLIMIT", "GOTRACEBACK",
	// git's switch that keeps a read-only command from taking optional
	// locks, so `git status`, and the go command's VCS stamp that runs it,
	// do not write .git/index.lock and .git/index; the gate sets it to 0.
	"GIT_OPTIONAL_LOCKS",
	ToolDigest,
}

// Environment returns the entries of environ that a cached Go test may
// receive, in their order.
func Environment(environ []string) []string {
	var kept []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(Names, name) {
			kept = append(kept, entry)
		}
	}
	return kept
}
