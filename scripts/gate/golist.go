package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
)

// goListMemo, when set, answers a go list the gate runs in its root
// directory from an earlier run of the same command in the same process.
// It is nil in the gate: each run declares its steps once, before any step
// writes the checkout. Its own tests set it in TestMain for the real
// checkout only, which no test edits: about 34 tests declare the same
// steps, and each declaration ran go list -deps -test over the whole
// module (GATE-PKG-TIMEOUT). A fixture checkout is never memoised: a test
// may edit it between two lists.
var goListMemo *listMemo

// listMemo holds the output of each go list run in root, by its argv and
// environment, and counts the runs it made and the lists it answered.
type listMemo struct {
	root       string
	mu         sync.Mutex
	out        map[string][]byte
	runs, hits int
}

func newListMemo(root string) *listMemo {
	return &listMemo{root: root, out: map[string][]byte{}}
}

// listOutput is command's output, as runOutput gives it, from the memo
// when command runs in the memo's root and ran before.
func listOutput(command *exec.Cmd) ([]byte, error) {
	m := goListMemo
	if m == nil || command.Dir != m.root {
		return runOutput(command)
	}
	environment := slices.Clone(command.Env)
	if environment == nil {
		environment = os.Environ()
	}
	slices.Sort(environment)
	key := command.Dir + "\x01" + strings.Join(command.Args, "\x00") + "\x01" + strings.Join(environment, "\x00")
	m.mu.Lock()
	defer m.mu.Unlock()
	if out, ok := m.out[key]; ok {
		m.hits++
		return slices.Clone(out), nil
	}
	out, err := runOutput(command)
	if err != nil {
		return out, err
	}
	m.runs++
	m.out[key] = slices.Clone(out)
	return out, nil
}
