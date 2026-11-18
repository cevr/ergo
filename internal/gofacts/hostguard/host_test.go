package hostguard

import (
	"os"
	"slices"
	"sync"
	"testing"

	"ergo.local/prototype/internal/gofacts"
)

// TestHostRowsHold is the guard of gofacts.Hosts (HOST-ROW): every row has
// a guarded call and passes it, and every guarded call is a row; three
// planted false rows, run through the rows' own checks, are refuted: a
// Getenv that keeps the value it read first (it does not follow the host
// state), an Environ that hands every caller one slice (its result is not
// the caller's) and a Getenv that leaves a goroutine running; and a
// ReadFile that keeps the bytes it read first (it reads the first state's
// file in the second state's directory: HOST-ROW-2).
//
// Mutants it alone catches: ffi-host-guard-one-state (the guard runs the
// call under one host state only), ffi-host-guard-shared-result (Environ's
// check writes nothing to its result) and ffi-host-guard-goroutine-blind
// (the guard counts no goroutines), ffi-host-file-state-blind (the file
// fixtures are the same in both states) and
// ffi-host-file-guards-unregistered (the file rows have no guarded call).
func TestHostRowsHold(t *testing.T) {
	var targets []string
	for _, e := range gofacts.Hosts {
		targets = append(targets, e.Target)
	}
	for target, err := range Hosts(targets) {
		t.Errorf("%s: %v", target, err)
	}
	for target := range hostCalls() {
		if _, ok := gofacts.HostOf(target); !ok {
			t.Errorf("%s: a guarded call with no row in gofacts.Hosts", target)
		}
	}
	var once, readOnce sync.Once
	var kept string
	var first []byte
	shared := []string{"HOME=/home/a", "HOME=/home/b"}
	stop := make(chan struct{})
	defer close(stop)
	planted := map[string]func(s ambient) error{
		"keeps its first read": getenvCheck(func(k string) string {
			once.Do(func() { kept = os.Getenv(k) })
			return kept
		}),
		"hands out one slice": environCheck(func() []string { return shared }),
		"leaves a goroutine": getenvCheck(func(k string) string {
			go func() { <-stop }()
			return os.Getenv(k)
		}),
		"keeps the file it read first": readFileCheck(func(name string) ([]byte, error) {
			var err error
			readOnce.Do(func() { first, err = os.ReadFile(name) })
			return slices.Clone(first), err
		}),
	}
	for name, call := range planted {
		if err := hostGuard(name, call); err == nil {
			t.Errorf("the planted false row %q passes the host guard", name)
		}
	}
}
