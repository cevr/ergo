package mygo

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"testing"
)

// liveRe is a gctrace line's heap sizes: at the start of the cycle, at its
// end, and the live heap it marked, in MB.
var liveRe = regexp.MustCompile(`(?m)^gc \d+ .* \d+->\d+->(\d+) MB`)

// settledLive is the live heap, in MB, of a run's gctrace: the least live
// heap of the last quarter of its collections, which is what stays
// reachable once a cycle's passing garbage (a frame's paint lists and
// clones) is gone.
func settledLive(trace string) (int, int, error) {
	var live []int
	for _, m := range liveRe.FindAllStringSubmatch(trace, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, 0, err
		}
		live = append(live, n)
	}
	if len(live) < 8 {
		return 0, len(live), fmt.Errorf("%d collections, want 8 or more", len(live))
	}
	return slices.Min(live[len(live)*3/4:]), len(live), nil
}

// TestDashGuiHeapStaysFlat is F-GP4c-1's growth guard on the desktop host:
// the dashboard's window binary, headless, keeps the same live heap after
// 4n frames as after n. The session's watcher posts a Committed at each
// state the actor commits; before the watcher read LatestChanges (cw-uc
// f522f5279) a watcher the full queue held kept every state the actor
// committed from then on, each with its own copy of the panels' rings
// (measured: 12 MB live at 150 frames and 20 MB at 600; with Drain's
// Pump, 12 and 13). The work is counted, not timed: the
// live heap the collector marks, at GOGC=25 so a run has hundreds of
// collections.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign), and the live heap is the process's.
func TestDashGuiHeapStaysFlat(t *testing.T) {
	bins, err := buildDash()
	if err != nil {
		t.Fatal(err)
	}
	const n = 150
	live := map[int]int{}
	for _, frames := range []int{n, 4 * n} {
		cmd := bounded(bins[1], "-headless", "-seed", "3", "-frames", strconv.Itoa(frames))
		cmd.Env = append(os.Environ(), "GOGC=25", "GODEBUG=gctrace=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dash-gui -headless -frames %d: %v\n%s", frames, err, out)
		}
		mb, gcs, err := settledLive(string(out))
		if err != nil {
			t.Fatalf("frames %d: %v", frames, err)
		}
		live[frames] = mb
		t.Logf("frames %d: %d collections, settled live heap %d MB", frames, gcs, mb)
	}
	// The panels' rings fill as the frames go (a chart keeps 10,000
	// samples), so the live heap may rise a little; a kept state per frame
	// adds 8 MB or more by 4n.
	if live[4*n] > live[n]+3 {
		t.Errorf("live heap %d MB after %d frames, %d MB after %d: the host keeps state per frame", live[4*n], 4*n, live[n], n)
	}
}
