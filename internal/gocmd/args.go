// Package gocmd is the toolchain front of `ergo go VERB ...` (GO-INTEROP
// route 1a, GO-DROPIN-PROBE): a Go module whose packages are Go or Ergo,
// one language per package (owner q296), built by the pinned go command
// as `go VERB ...` would build it. Each Ergo package is checked and
// lowered as a Go library (lower.LowerIRLibrary) into ergo's cache, and
// the go command sees it through its own flags: -overlay puts the
// package's declaration file in the package's folder, -gcflags gives its
// compile the IR (-ergoir), and -modfile adds the runtime module to a copy
// of go.mod. Nothing is written in the user's tree and Go's own files are
// not patched beyond the one -ergoir hook (MINIMAL FORK PATCH).
//
// This file reads the go command line as cmd/go does, far enough to merge
// ergo's flags with the user's: which arguments are packages, and the
// -gcflags, -overlay and -modfile the user gave on the line or in GOFLAGS.
package gocmd

import (
	"fmt"
	"strings"
)

// BuildVerbs are the go commands that take the build flags: `ergo go`
// adds its flags to these and runs every other verb as it is.
var BuildVerbs = map[string]bool{"build": true, "install": true, "list": true, "run": true, "test": true, "vet": true}

// valueFlags are the go command's flags of these verbs that take a value
// as the next argument when not written -flag=value (cmd/go's build,
// list, run, test and vet flags; a test binary flag go test knows).
var valueFlags = map[string]bool{
	"C": true, "p": true, "o": true, "asmflags": true, "buildmode": true, "compiler": true, "gccgoflags": true,
	"gcflags": true, "installsuffix": true, "ldflags": true, "mod": true, "modfile": true, "overlay": true,
	"pgo": true, "pkgdir": true, "tags": true, "toolexec": true, "covermode": true, "coverpkg": true, "exec": true,
	"f": true, "vettool": true, "reuse": true,
	"run": true, "skip": true, "bench": true, "benchtime": true, "count": true, "cpu": true, "parallel": true,
	"timeout": true, "list": true, "shuffle": true, "fuzz": true, "fuzztime": true, "fuzzminimizetime": true,
	"coverprofile": true, "cpuprofile": true, "memprofile": true, "memprofilerate": true, "blockprofile": true,
	"blockprofilerate": true, "mutexprofile": true, "mutexprofilefraction": true, "outputdir": true, "trace": true,
	"vet": true,
}

// Line is one go command line of a build verb, read as cmd/go reads it.
type Line struct {
	Verb string
	// Args are the arguments after the verb with the user's -gcflags,
	// -overlay and -modfile taken out (they come back merged, Assemble).
	Args []string
	// GCFlags are the values of the user's -gcflags, GOFLAGS' first, in
	// the order cmd/go applies them.
	GCFlags []string
	// Overlay and Modfile are the user's, "" when not given; the line's
	// own override GOFLAGS', as in cmd/go.
	Overlay, Modfile string
	// Packages are the package arguments (patterns, import paths, folders
	// or .go files): what cmd/go marks as named on the command line.
	Packages []string
}

// Parse reads args, the arguments after `go`, and goflags, the GOFLAGS
// environment value. The verb is args[0] and is a build verb.
func Parse(args []string, goflags string) (Line, error) {
	if len(args) == 0 || !BuildVerbs[args[0]] {
		return Line{}, fmt.Errorf("gocmd: %q is not a build verb", strings.Join(args, " "))
	}
	l := Line{Verb: args[0]}
	for _, f := range strings.Fields(goflags) {
		name, value, has := cutFlag(f)
		if !has {
			continue
		}
		switch name {
		case "gcflags":
			l.GCFlags = append(l.GCFlags, value)
		case "overlay":
			l.Overlay = value
		case "modfile":
			l.Modfile = value
		}
	}
	rest := args[1:]
	program := false // run: the arguments after the package are the program's
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if program {
			l.Args = append(l.Args, a)
			continue
		}
		if a == "-args" && l.Verb == "test" || a == "--" {
			l.Args = append(l.Args, rest[i:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			l.Args = append(l.Args, a)
			if l.Verb == "run" && len(l.Packages) > 0 && !strings.HasSuffix(a, ".go") {
				// The first argument after run's .go files.
				program = true
				continue
			}
			l.Packages = append(l.Packages, a)
			if l.Verb == "run" && !strings.HasSuffix(a, ".go") {
				program = true
			}
			continue
		}
		name, value, has := cutFlag(a)
		if !has && valueFlags[name] && i+1 < len(rest) {
			value, has = rest[i+1], true
			i++
			if name != "gcflags" && name != "overlay" && name != "modfile" {
				l.Args = append(l.Args, a, value)
				continue
			}
		}
		switch {
		case name == "gcflags" && has:
			l.GCFlags = append(l.GCFlags, value)
		case name == "overlay" && has:
			l.Overlay = value
		case name == "modfile" && has:
			l.Modfile = value
		default:
			l.Args = append(l.Args, a)
		}
	}
	return l, nil
}

// cutFlag is the name and value of a flag argument -name[=value] or
// --name[=value].
func cutFlag(a string) (name, value string, has bool) {
	a = strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
	name, value, has = strings.Cut(a, "=")
	return name, value, has
}
