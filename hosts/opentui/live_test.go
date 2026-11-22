package opentui

import (
	"flag"
	"math/rand/v2"
	"strings"
	"testing"
)

var liveSeed = flag.Uint64("live.seed", 0, "run TestLiveRegionSaysWhatTheTestHostAnnounces on this seed only")

// TestLiveRegionSaysWhatTheTestHostAnnounces is gap 7's acceptance: the
// terminal driver says on the terminal's notification channel exactly the
// texts ui.TestHost announces for a live region, in order, over seeded
// scripts of taps (cmd/livesay) that insert the region, set it to new
// texts, to the same text, to the empty one, and remove it. The first
// pass says nothing on either host.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it. A failing seed
// prints its script and the replay command.
func TestLiveRegionSaysWhatTheTestHostAnnounces(t *testing.T) {
	bin, err := build("livesay", "./cmd/livesay")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{}
	if *liveSeed != 0 {
		seeds = append(seeds, *liveSeed)
	} else {
		for s := uint64(1); s <= 20; s++ {
			seeds = append(seeds, s)
		}
	}
	failed, steps := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6c697665))
		var lines []string
		for range 4 + r.IntN(16) {
			lines = append(lines, "tap "+pick(r, "s0", "s1", "s2", "s3", "toggle"))
		}
		script := strings.Join(lines, "\n") + "\n"
		out, err := play(bin, script)
		var bad []string
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "ok "):
				steps++
			case line == "end":
			default:
				bad = append(bad, line)
			}
		}
		if err != nil || len(bad) > 0 || !strings.HasSuffix(out, "end\n") {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d: %v\n%s\nscript:\n%sreplay: go test -run TestLiveRegionSaysWhatTheTestHostAnnounces -live.seed=%d", seed, err, strings.Join(bad, "\n"), script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d steps agree", len(seeds), steps)
}
