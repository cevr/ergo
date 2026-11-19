package opentui

import (
	"strings"
	"testing"
)

// TestNoFrameWhenSettled: on the spin app (cmd/hostagree), a frame whose
// pass changes nothing on the screen makes no library call and writes no
// byte. After the spin's 12 frames the view stops animating; then 30 more
// frames of time make no call and write no byte, and TestHost and the
// terminal agree at every step on the frames stepped (ui.Frames).
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it, and hostagree is the
// program that does.
//
// Mutants: a Tick whose pass changes nothing still paints (the driver
// presents an empty patch), and the frames run while no pass animates.
func TestNoFrameWhenSettled(t *testing.T) {
	bin, err := build("hostagree", "./cmd/hostagree")
	if err != nil {
		t.Fatal(err)
	}
	script := "spin\ntap go\ncost frames 1\nframes 20\ncost frames 30\n"
	text, err := play(bin, script)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	var costs []string
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "cost "):
			costs = append(costs, line)
		case strings.HasPrefix(line, "ok "), line == "end":
		default:
			t.Errorf("%s", line)
		}
	}
	want := []string{"cost 0 bytes 0", "cost 0 bytes 0"}
	if strings.Join(costs, "\n") != strings.Join(want, "\n") {
		t.Errorf("the unchanged frame, then the 30 settled frames, cost:\n%s\nwant:\n%s\nscript:\n%s", strings.Join(costs, "\n"), strings.Join(want, "\n"), script)
	}
}
