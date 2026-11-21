package mygo

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadoutsOnTheDesktop is LIVE on the window (stdlib.md, UI 24):
// cmd/clock-gui -headless publishes 120 readings of its time and its
// level through a headless MyGo window, then a lap runs a pass. The lap
// button, which sits beside the level readout in a row, is where it was
// before any reading, since a readout lays out by its reserve
// (mutant live-desktop-box-follows-reading); each reading's wake paints
// the window (mutant live-desktop-reading-unpainted); the readouts
// read the last readings; and the window's last paint drew those
// readings, not a kept draw of an earlier one (mutant
// UIB24-reading-kept-stale).
//
// It builds and runs a Go binary because the window driver is Ergo source
// that calls Go (go.Foreign): no in-process check runs it, and
// ui.TestHost, where the readout laws run, lays nothing out.
func TestReadoutsOnTheDesktop(t *testing.T) {
	ergo, err := builtErgo()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(filepath.Dir(ergo), "clock-gui")
	if out, err := bounded(ergo, "build", "./cmd/clock-gui", "-o", bin).CombinedOutput(); err != nil {
		t.Fatalf("ergo build clock-gui: %v\n%s", err, out)
	}
	const n = 120
	out, err := bounded(bin, "-headless", fmt.Sprint(n)).CombinedOutput()
	if err != nil {
		t.Fatalf("clock-gui -headless %d: %v\n%s", n, err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 4 {
		t.Fatalf("clock-gui -headless %d printed %d lines, want 4:\n%s", n, len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "lap at ") || lines[1] != lines[0] {
		t.Errorf("the lap button moved: %q before the readings, %q after them and a pass", lines[0], lines[1])
	}
	var clock, level string
	var laps, painted int
	if _, err := fmt.Sscanf(lines[2], "%s %s laps %d painted %d", &clock, &level, &laps, &painted); err != nil {
		t.Fatalf("last line %q: %v", lines[2], err)
	}
	// 120 readings at 60 a second are 2 seconds; the level saws to 100%.
	if clock != "00:02.00" || level != fmt.Sprintf("%d%%", n%101) || laps != 1 {
		t.Errorf("clock-gui shows %q %q with %d laps, want \"00:02.00\" \"%d%%\" with 1", clock, level, laps, n%101)
	}
	// The window drew what the readouts read: the last line is the texts
	// its paint after the last reading drew, kept draws too, before the
	// lap's pass could draw the readouts again.
	drawn := strings.Split(lines[3], "|")
	for _, r := range []string{clock, level} {
		found := false
		for _, s := range drawn[1:] {
			found = found || s == r
		}
		if drawn[0] != "drew" || !found {
			t.Errorf("the window's last paint drew %q, not the reading %q", lines[3], r)
		}
	}
	// One paint for the first pass, then one for each of the 2n wakes.
	if painted < 2*n+1 {
		t.Errorf("the window painted %d times for %d readings of two readouts, want at least %d", painted, n, 2*n+1)
	}
}
