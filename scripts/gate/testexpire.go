package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Expiry of Go test results (gate-testcache-gap spec Q-R5-1). Before its
// first step, each gate run expires every Go test result that a process
// started by a step can reach, with Go's own mechanism: go clean -testcache
// writes testexpire.txt in a cache directory, and go test ignores every
// cached result older than that time. The build cache stays warm; only test
// results expire. So no go test that a step starts, at any depth and with any
// environment the gate's own guards cannot see, replays a pass recorded
// before the run.
//
// A child reaches a cache through its GOCACHE. A child that inherits its
// environment gets the step's; a child that builds its own environment from
// a list of names (ordinary process isolation) may drop GOCACHE, and then
// XDG_CACHE_HOME, and Go takes the next default (cmd/go's cache.DefaultDir:
// os.UserCacheDir, then $HOME/.cache, and the GOENV file can name one too).
// So the reachable caches are what go env GOCACHE gives under the step's
// environment and under its two reduced forms (reducedEnvironments). A child
// that points GOCACHE at a new, empty directory has no earlier result to
// replay.
//
// The uncached default (uncachedTestFlag) stays: it stops a child that
// inherits the step's environment from saving a result within the run. The
// expiry covers the results saved before the run, whatever the child's
// environment.
//
// Out of scope (docs/tooling.md, "Go's test cache"): a child that points
// GOCACHE at a persistent directory of its own choosing, and an input that a
// step changes during the run after another child in the same run saved a
// result that read it.

// cacheReductions are the variables each reduced form of a step's
// environment drops, in order: the step's environment itself, then without
// GOCACHE, then without GOCACHE and XDG_CACHE_HOME.
var cacheReductions = [][]string{nil, {"GOCACHE"}, {"GOCACHE", "XDG_CACHE_HOME"}}

// reducedEnvironments are the environments a child of a step may plausibly
// run with, as far as its Go cache is concerned (cacheReductions).
func reducedEnvironments(environment []string) [][]string {
	var forms [][]string
	for _, drop := range cacheReductions {
		forms = append(forms, slices.DeleteFunc(slices.Clone(environment), func(entry string) bool {
			name, _, _ := strings.Cut(entry, "=")
			return slices.Contains(drop, name)
		}))
	}
	return forms
}

// testCache is one Go cache directory a step's children can reach, with the
// environment that resolved it, under which the gate expires it.
type testCache struct {
	dir         string
	environment []string
}

// reachableTestCaches returns the distinct Go cache directories that the
// children of steps can reach: go env GOCACHE, run in the checkout under each
// step's command environment (commandEnvironment) and its reduced forms. A
// form that has no cache ("off") reaches none. An error, or a directory that
// is not absolute, fails the run: the gate cannot say which results it would
// replay.
func (g *gate) reachableTestCaches(steps []*Step) ([]testCache, error) {
	var environments [][]string
	seen := map[string]bool{}
	for _, step := range steps {
		for _, form := range reducedEnvironments(g.commandEnvironment(step)) {
			id := strings.Join(form, "\x00")
			if !seen[id] {
				seen[id] = true
				environments = append(environments, form)
			}
		}
	}
	dirs := make([]string, len(environments))
	errs := make([]error, len(environments))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, environment := range environments {
		wg.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			dirs[i], errs[i] = g.goCommand(environment, "env", "GOCACHE")
		})
	}
	wg.Wait()
	var caches []testCache
	for i, dir := range dirs {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if dir == "off" || slices.ContainsFunc(caches, func(c testCache) bool { return c.dir == dir }) {
			continue
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("go env GOCACHE gives %q, not an absolute directory", dir)
		}
		caches = append(caches, testCache{dir: dir, environment: environments[i]})
	}
	slices.SortFunc(caches, func(a, b testCache) int { return strings.Compare(a.dir, b.dir) })
	return caches, nil
}

// expireTestResults expires the Go test results in every cache the steps'
// children can reach (reachableTestCaches), each with go clean -testcache
// under the environment that resolved it, and returns the one log line that
// names them and the time.
func (g *gate) expireTestResults(steps []*Step) (string, error) {
	started := time.Now()
	caches, err := g.reachableTestCaches(steps)
	if err != nil {
		return "", err
	}
	errs := make([]error, len(caches))
	var wg sync.WaitGroup
	for i, cache := range caches {
		wg.Go(func() {
			_, errs[i] = g.goCommand(cache.environment, "clean", "-testcache")
		})
	}
	wg.Wait()
	var dirs []string
	for i, cache := range caches {
		if errs[i] != nil {
			return "", errs[i]
		}
		dirs = append(dirs, cache.dir)
	}
	return fmt.Sprintf("gate: expired Go test results before %s in %d caches (%.2fs): %s", started.UTC().Format(time.RFC3339Nano), len(dirs), time.Since(started).Seconds(), strings.Join(dirs, " ")), nil
}

// goCommand runs go with args in the checkout under environment and returns
// its trimmed standard output.
func (g *gate) goCommand(environment []string, args ...string) (string, error) {
	command := exec.Command("go", args...)
	command.Dir = g.root
	command.Env = environment
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("go %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output)), nil
}
