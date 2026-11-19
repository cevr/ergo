//go:build !race && !ergo_stockpark

package rt

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func init() {
	parkBodies["word"] = func() lawParker { return new(wordParker) }
}

// TestParkWordLinksTheRuntime checks the word body's view of the Go
// runtime against the source of the release this test is built with:
// gopark's and goready's signatures (a g passed as its address, the two
// reasons as uint8), and the numbers of waitReasonSyncCondWait and
// traceBlockCondWait. A release that changes one of them fails here
// before a program parks on a wrong signature.
//
// Mutants (testdata/mutants.json): park-reason-renumbered,
// park-trace-reason-renumbered.
func TestParkWordLinksTheRuntime(t *testing.T) {
	src := filepath.Join(runtime.GOROOT(), "src", "runtime")
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("the runtime source of %s: %v", runtime.Version(), err)
		}
		return string(data)
	}
	proc := read("proc.go")
	for _, decl := range []string{
		"func gopark(unlockf func(*g, unsafe.Pointer) bool, lock unsafe.Pointer, reason waitReason, traceReason traceBlockReason, traceskip int) {",
		"func goready(gp *g, traceskip int) {",
	} {
		if !strings.Contains(proc, "\n"+decl+"\n") {
			t.Errorf("%s: runtime/proc.go has no %q; park_word.go's linknames would not match", runtime.Version(), decl)
		}
	}
	rt2 := read("runtime2.go")
	tr := read("traceruntime.go")
	for _, c := range []struct {
		src, typ, name string
		want           int
	}{
		{rt2, "waitReason", "waitReasonSyncCondWait", parkWaitReason},
		{tr, "traceBlockReason", "traceBlockCondWait", parkTraceReason},
	} {
		if !strings.Contains(c.src, "\ntype "+c.typ+" uint8\n") {
			t.Errorf("%s: %s is not a uint8", runtime.Version(), c.typ)
		}
		if got := iotaOf(c.src, c.typ, c.name); got != c.want {
			t.Errorf("%s: %s is %d in the runtime; park_word.go says %d", runtime.Version(), c.name, got, c.want)
		}
	}
}

// iotaOf is the number of name in the const block that starts with
// "<first> <typ> = iota": its line index among the block's names.
func iotaOf(src, typ, name string) int {
	start := regexp.MustCompile(`(?m)^\t\w+\s+` + typ + ` = iota`).FindStringIndex(src)
	if start == nil {
		return -1
	}
	block, _, _ := strings.Cut(src[start[0]:], "\n)")
	n := 0
	for _, line := range strings.Split(block, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "//") {
			continue
		}
		if f[0] == name {
			return n
		}
		n++
	}
	return -1
}
