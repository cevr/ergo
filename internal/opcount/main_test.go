package opcount_test

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

// childMode makes this test binary a child program of the counter tests:
// "loop:K" runs K steps and exits 0; "spawn:K" runs a child "loop:K", waits
// for it, then runs K steps itself; "exit:N" exits N; "orphan" starts a
// child "spin" and exits without waiting; "spin" runs seconds of steps.
const childMode = "OPCOUNT_TEST_CHILD"

var sink uint64

func loop(k int) {
	x := sink
	for i := range k {
		x = x*6364136223846793005 + uint64(i)
	}
	sink = x
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(childMode); mode != "" {
		os.Exit(child(mode))
	}
	os.Exit(testinputs.Main(m))
}

func child(mode string) int {
	name, arg, _ := strings.Cut(mode, ":")
	n, _ := strconv.Atoi(arg)
	switch name {
	case "loop":
		loop(n)
	case "spawn":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), childMode+"=loop:"+arg)
		if cmd.Run() != nil {
			return 3
		}
		loop(n)
	case "exit":
		return n
	case "orphan":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), childMode+"=spin")
		if cmd.Start() != nil {
			return 3
		}
	case "spin":
		// Seconds of work, so it is still running when Count checks the
		// group; it ends by itself if a mutant leaves it running.
		loop(1 << 33)
	}
	return 0
}
