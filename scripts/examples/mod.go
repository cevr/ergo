package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// The mod leg runs first for a program tagged mod: a program of packages
// whose go.mod requires other Ergo modules (grammar.md, Source files 1,
// Required modules). The run reads no network, so the module cache must be
// warm: `go mod verify` in the program's folder, with GOPROXY=off, checks
// that every module of the build list is in the cache and that its copy
// is the one downloaded. A cold or changed cache fails the program, which
// never skips; the build leg's loader then checks each copy against
// go.sum. Warm the cache with ergo get in the program's folder
// (docs/tooling.md, ergo get).

// modEnv is the environment of the mod leg: the runner's, with no proxy
// (a module not in the cache fails, never downloads) and go.mod read as it
// is.
func modEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GOPROXY=") || strings.HasPrefix(kv, "GOFLAGS=")
	})
	return append(env, "GOPROXY=off", "GOFLAGS=-mod=mod")
}

// modLeg checks the program's module cache; false when it failed the
// program.
func (r *runner) modLeg(p *program) bool {
	o, err := r.live.execute([]string{"go", "mod", "verify"}, p.dir, modEnv(), "", r.cfg.timeout, 0)
	if err == nil && o.passed() {
		return true
	}
	fmt.Fprintf(r.out, "FAIL %s: mod: the module cache does not hold the modules go.mod requires, as downloaded (go mod verify, GOPROXY=off, exit %s%s); warm it with ergo get MODULE@VERSION in %s\n", p.name, o.statusText(), o.why(r.cfg.timeout), p.dir)
	r.printOutput(o, err)
	r.fail(p.name + ": mod")
	return false
}
