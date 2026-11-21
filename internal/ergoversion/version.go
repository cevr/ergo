// Package ergoversion is the version of this ergo binary: the Go module
// version of the ergo module it was built from (docs/tooling.md, Which ergo
// a module needs). A module's ergo.mod line `ergo VERSION` is compared with
// it (internal/sketchfront, parseModule).
package ergoversion

import (
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Devel is the version of a binary that carries no module version and no
// VCS stamp: a build with -buildvcs=false (the gate's builds), a test
// binary, a build outside a repository. It accepts every ergo line.
const Devel = "devel"

// Current is the version of the executing binary, read once.
var Current = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Devel
	}
	return Of(info)
})

// Of is the version that info describes: the main module's version when the
// go command stamped one (`go install …@VERSION`, or a build in a Git
// checkout, which Go stamps with a pseudo-version, +dirty when the tree is
// modified); else the pseudo-version of the vcs.revision and vcs.time
// settings; else Devel. The producer declaration (internal/producer) shows
// the same string.
func Of(info *debug.BuildInfo) string {
	if info == nil {
		return Devel
	}
	if v := info.Main.Version; v != "" && v != "(devel)" && Valid(v) == "" {
		return v
	}
	var revision, when, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			when = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	at, err := time.Parse(time.RFC3339, when)
	if len(revision) < 12 || err != nil {
		return Devel
	}
	v := "v0.0.0-" + at.UTC().Format("20060102150405") + "-" + revision[:12]
	if modified == "true" {
		v += "+dirty"
	}
	if Valid(v) != "" {
		return Devel
	}
	return v
}

// InstallCommand is the command that installs ergo at version.
func InstallCommand(version string) string {
	return "go install github.com/cevr/ergo/cmd/ergo@" + version
}

// Valid is why v is not a semantic version with Go's leading v
// (semver.org 2.0.0: MAJOR.MINOR.PATCH, numbers without leading zeros, an
// optional pre-release of dot-separated identifiers, optional build
// metadata); "" when it is one.
func Valid(v string) string {
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return "it does not start with v"
	}
	rest, build, hasBuild := strings.Cut(rest, "+")
	core, pre, hasPre := strings.Cut(rest, "-")
	numbers := strings.Split(core, ".")
	if len(numbers) != 3 {
		return "it is not vMAJOR.MINOR.PATCH"
	}
	for _, n := range numbers {
		if !numeral(n) {
			return "its numbers are not decimal numbers without leading zeros"
		}
	}
	if hasPre && !identifiers(pre, true) {
		return "its pre-release is not dot-separated identifiers of letters, digits and -"
	}
	if hasBuild && !identifiers(build, false) {
		return "its build metadata is not dot-separated identifiers of letters, digits and -"
	}
	return ""
}

func numeral(s string) bool {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// identifiers reports whether s is dot-separated nonempty identifiers of
// ASCII letters, digits and -; a pre-release's numeric identifier has no
// leading zero.
func identifiers(s string, pre bool) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		digits := true
		for i := 0; i < len(id); i++ {
			c := id[i]
			switch {
			case '0' <= c && c <= '9':
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', c == '-':
				digits = false
			default:
				return false
			}
		}
		if pre && digits && !numeral(id) {
			return false
		}
	}
	return true
}
