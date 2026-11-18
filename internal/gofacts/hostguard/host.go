package hostguard

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"time"
)

// The guard of the host rows (gofacts.Hosts; HOST-ROW): each row's call
// runs under each of the determinism guard's two host states (pure.go),
// which differ in the working directory, HOME, the local zone and
// GOMAXPROCS. The call must follow the state it is said to read (Getenv
// gives that state's HOME, Getwd its directory, Now its zone), give a
// slice result the caller owns (a write to it is not seen by the next
// call), and leave no goroutine behind. That it reads no other memory and
// takes no callback is the reviewer's, as a pure row's completeness is.

// unsetKey is an environment variable no host state sets.
const unsetKey = "ERGO_HOSTGUARD_UNSET"

// hostCalls are the guarded calls, one per row of gofacts.Hosts: each
// checks the call under the host state s, entered.
func hostCalls() map[string]func(s ambient) error {
	calls := map[string]func(s ambient) error{
		"os.Getenv": getenvCheck(os.Getenv),
		"os.LookupEnv": func(s ambient) error {
			if v, ok := os.LookupEnv("HOME"); v != s.home || !ok {
				return fmt.Errorf("LookupEnv(HOME) is (%q, %v) under HOME=%q", v, ok, s.home)
			}
			if v, ok := os.LookupEnv(unsetKey); v != "" || ok {
				return fmt.Errorf("LookupEnv(%s) is (%q, %v), unset", unsetKey, v, ok)
			}
			return nil
		},
		"os.Environ": environCheck(os.Environ),
		"os.Getwd": func(s ambient) error {
			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			got, err1 := filepath.EvalSymlinks(wd)
			want, err2 := filepath.EvalSymlinks(s.dir)
			if err1 != nil || err2 != nil || got != want {
				return fmt.Errorf("Getwd() is %q in %q", wd, s.dir)
			}
			return nil
		},
		"time.Now": func(s ambient) error {
			a := time.Now()
			b := time.Now()
			if b.Before(a) || a.Location() != s.local {
				return fmt.Errorf("Now() went from %v to %v, zone %v under %v", a, b, a.Location(), s.local)
			}
			return nil
		},
	}
	maps.Copy(calls, fileCalls()) // files.go
	// The open-file rows (handles.go).
	maps.Copy(calls, handleCalls())
	return calls
}

// getenvCheck is the check of a Getenv: it gives the state's HOME.
func getenvCheck(getenv func(string) string) func(s ambient) error {
	return func(s ambient) error {
		if v := getenv("HOME"); v != s.home {
			return fmt.Errorf("Getenv(HOME) is %q under HOME=%q", v, s.home)
		}
		return nil
	}
}

// environCheck is the check of an Environ: it holds the state's HOME, and
// its result is the caller's, so a write to it is not seen by the next
// call.
func environCheck(environ func() []string) func(s ambient) error {
	return func(s ambient) error {
		first := environ()
		if !slices.Contains(first, "HOME="+s.home) {
			return fmt.Errorf("Environ() has no HOME=%s", s.home)
		}
		fresh := slices.Clone(first)
		for i := range first {
			first[i] = "ERGO_HOSTGUARD=written"
		}
		if again := environ(); !slices.Equal(again, fresh) {
			return fmt.Errorf("Environ() after a write to its last result is %d entries, %v first; the result is not the caller's", len(again), again[:min(len(again), 1)])
		}
		return nil
	}
}

// hostGuard runs call under each of the two host states and gives its
// first failure: a check that failed, or a goroutine the call left.
func hostGuard(target string, call func(s ambient) error) error {
	a, b, done, err := pureStates()
	if err != nil {
		return err
	}
	defer done()
	for _, s := range []ambient{a, b} {
		restore, err := s.enter()
		if err != nil {
			return err
		}
		before := runtime.NumGoroutine()
		err = call(s)
		after := runtime.NumGoroutine()
		restore()
		switch {
		case err != nil:
			return fmt.Errorf("%s: %w", target, err)
		case after != before:
			return fmt.Errorf("%s: %d goroutines before the call, %d after it", target, before, after)
		}
	}
	return nil
}

// Hosts runs every host row's guard: the refuted targets, each with its
// failure, in a map; a row with no guarded call is refuted.
func Hosts(targets []string) map[string]error {
	calls := hostCalls()
	out := map[string]error{}
	sorted := slices.Clone(targets)
	sort.Strings(sorted)
	for _, t := range sorted {
		call, ok := calls[t]
		if !ok {
			out[t] = fmt.Errorf("%s: a row of gofacts.Hosts with no guarded call in hostguard", t)
			continue
		}
		if err := hostGuard(t, call); err != nil {
			out[t] = err
		}
	}
	return out
}
