package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDashGuiAgreesWithControl is the agreement property of
// examples/dash-gui (corpus wave GP4a): on seeded sensor banks and
// scripts, the Ergo program (a task and a bounded queue for each source)
// and the hand Go control (testdata/dash-gui/control: a goroutine and a
// buffered channel for each source) print exactly what a plain reference
// model prints (dashReference: one loop, no queues, each frame's kept
// samples cut from the frame's samples by the drop policy), stdout,
// stderr and exit status. The inputs reach the edges: runs of 0 frames
// and runs that end on either side of the stale bound, the silent
// stretches' edges and the bursts past a queue's capacity, pauses of
// paused and stale panels, resumes of running ones, panels past the
// last, policy changes in mid-run, and lines that are no command. Every
// fifth seed also plays a scene on the view (dash-gui scene: the app
// machine under ui.TestHost, a frame of the clock a board frame through
// feed.Frame's tasks-free path), whose text nodes must be the reference's
// frames, with button presses read as pauses, resumes and policy swaps. A
// failing seed prints its replay command and the first line that
// differs.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program runs its sources as tasks and reads its standard
// input, and the control is Go; no in-process check runs either. Each
// run is bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/dash-gui/
// testdata/mutants.json): see the ledger's cmd/ergo killers.
func TestDashGuiAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildCorpusProgram(t, "dash-gui")
	control := buildCorpusControl(t, work, "dash-gui")
	seeds := 150
	first := uint64(1)
	if s := os.Getenv("DASH_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("DASH_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0xda5b))
		bank := r.IntN(2000) - 100
		script := dashScript(r)
		want := corpusOutput(dashReference(bank, script), "", 0)
		args := []string{"play", "-seed", strconv.Itoa(bank)}
		for _, bin := range []string{ergoBin, control} {
			got := runCorpus(t, bin, t.TempDir(), args, script)
			if got != want {
				t.Fatalf("seed %d: %s printed other text than the reference\n%s\nreplay: DASH_SEED=%d go test -count=1 -run '^TestDashGuiAgreesWithControl$' ./cmd/ergo",
					seed, filepath.Base(bin), firstDiff(got, want), seed)
			}
		}
		// Every fifth seed also plays a scene on the view (ui.TestHost):
		// its text nodes are what the reference shows, with each button
		// press read as the reference's pause, resume or policy swap.
		// Scenes are short, since a frame of the view costs far more than
		// a frame of play (GP/bench-pending.md).
		if seed%5 == 0 || seeds == 1 {
			scene := dashScene(r)
			want := corpusOutput(dashReference(bank, dashSceneAsPlay(scene)), "", 0)
			got := runCorpus(t, ergoBin, t.TempDir(), []string{"scene", "-seed", strconv.Itoa(bank)}, scene)
			if got != want {
				t.Fatalf("seed %d: the scene printed other text than the reference\n%s\nreplay: DASH_SEED=%d go test -count=1 -run '^TestDashGuiAgreesWithControl$' ./cmd/ergo",
					seed, firstDiff(got, want), seed)
			}
		}
	}
}

// dashScene is a seed's scene script: about 300 frames in short runs,
// presses of every panel's buttons and of labels on no button.
func dashScene(r *rand.Rand) string {
	var b strings.Builder
	frames := 0
	for frames < 300 {
		switch k := r.IntN(20); {
		case k < 7:
			n := []int{0, 1, 2, 30, 59, 60, 61, 62}[r.IntN(8)]
			frames += n + 1
			fmt.Fprintf(&b, "frames %d\n", n)
		case k < 10:
			fmt.Fprintf(&b, "tap toggle %d\n", r.IntN(13))
		case k < 12:
			fmt.Fprintf(&b, "tap policy %d\n", r.IntN(13))
		case k < 15:
			b.WriteString("show\n")
		default:
			b.WriteString([]string{"", "# note", "fly", "frames", "frames -1", "frames x", "frames 1 2", "frames +2", "tap toggle 01", "tap knob", "tap panel 3"}[r.IntN(11)] + "\n")
		}
	}
	b.WriteString("show\n")
	return b.String()
}

// dashSceneAsPlay is scene as a script of dashReference: frames are runs,
// a show is a show and its drops, a press of a panel's button is its
// toggle or swap, a press of a canvas does nothing, and the rest is the
// text the scene writes for it ("echo").
func dashSceneAsPlay(scene string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(scene, "\n"), "\n") {
		words := strings.Split(line, " ")
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case words[0] == "frames":
			arg := ""
			if len(words) == 2 {
				arg = words[1]
			}
			if n, err := strconv.Atoi(arg); err == nil && n >= 0 {
				fmt.Fprintf(&b, "run %d\n", n)
			} else {
				b.WriteString("echo ? \"" + line + "\": want frames N\n")
			}
		case line == "show":
			b.WriteString("show\ndrops\n")
		case words[0] == "tap":
			label := strings.TrimPrefix(line, "tap ")
			n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(label, "toggle "), "policy "), "panel "))
			switch {
			case err != nil || n < 0 || n >= 12 || strconv.Itoa(n) != label[strings.LastIndex(label, " ")+1:] || !strings.Contains(label, " "):
				b.WriteString("echo ? no node " + label + "\n")
			case strings.HasPrefix(label, "toggle "):
				fmt.Fprintf(&b, "toggle %d\n", n)
			case strings.HasPrefix(label, "policy "):
				fmt.Fprintf(&b, "swap %d\n", n)
			}
		default:
			b.WriteString("echo ? \"" + line + "\": want frames, tap, show or shot\n")
		}
	}
	return b.String()
}

// dashScript is a seed's script: about 2,000 frames in runs that often
// end near the stale bound and the silent stretches' edges.
func dashScript(r *rand.Rand) string {
	var b strings.Builder
	frames := 0
	for frames < 2500 {
		switch k := r.IntN(30); {
		case k < 10:
			n := []int{0, 1, 2, 59, 60, 61, 62, 119, 180, 181, 240}[r.IntN(11)] + r.IntN(3)
			if r.IntN(3) == 0 {
				n = r.IntN(400)
			}
			frames += n + 1
			fmt.Fprintf(&b, "run %d\n", n)
		case k < 13:
			fmt.Fprintf(&b, "pause %d\n", r.IntN(14))
		case k < 16:
			fmt.Fprintf(&b, "resume %d\n", r.IntN(14))
		case k < 19:
			fmt.Fprintf(&b, "policy %d %s\n", r.IntN(14), []string{"oldest", "newest", "newest"}[r.IntN(3)])
		case k < 22:
			b.WriteString("show\n")
		case k < 24:
			b.WriteString("drops\n")
		default:
			b.WriteString([]string{"", "# note", "fly", "run", "run -1", "run +3", "run x", "run  2", "pause", "policy 1", "policy 1 middle", "policy -1 newest", "show me", "drops 0", "pause 1 2"}[r.IntN(15)] + "\n")
		}
	}
	b.WriteString("show\ndrops\n")
	return b.String()
}

// dashReference is what dash-gui play prints for bank's sensors and
// script, computed in one loop: each frame's samples are read in full,
// and the policy keeps the first or the last 16 of them.
func dashReference(bank int, script string) string {
	type entry struct{ frame, value int }
	type panelRef struct {
		kind            string
		mode            string
		got, seen       int
		samples         []int
		keep            int
		log             []entry
		from, to, start int
	}
	hash := func(seed, a, b, c int) uint64 {
		x := uint64(seed)*0x9E3779B97F4A7C15 ^ uint64(a)*0xBF58476D1CE4E5B9 ^ uint64(b)*0x94D049BB133111EB ^ uint64(c)*0xD6E8FEB86659FD93
		x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
		x = (x ^ (x >> 27)) * 0x94D049BB133111EB
		return x ^ (x >> 31)
	}
	ease := func(from, to, k int) int {
		const d = 30
		if k >= d {
			return to
		}
		if k <= 0 {
			return from
		}
		rest := d - k
		return from + (to-from)*(d*d*d-rest*rest*rest)/(d*d*d)
	}
	columns := func(xs []int) string {
		cols := min(len(xs), 8)
		var words []string
		for c := range cols {
			part := xs[c*len(xs)/cols : (c+1)*len(xs)/cols]
			lo, hi := part[0], part[0]
			for _, x := range part {
				lo, hi = min(lo, x), max(hi, x)
			}
			words = append(words, fmt.Sprintf("%d-%d", lo, hi))
		}
		return "[" + strings.Join(words, " ") + "]"
	}
	var panels []*panelRef
	for _, k := range []string{"chart", "chart", "chart", "chart", "spark", "spark", "spark", "gauge", "gauge", "strip", "strip", "log"} {
		panels = append(panels, &panelRef{kind: k, mode: "live", keep: map[string]int{"chart": 10000, "spark": 64, "strip": 256}[k]})
	}
	newest := make([]bool, len(panels))
	dropped := make([]int, len(panels))
	frame := 0
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(want string) { out.WriteString("? \"" + line + "\": want " + want + "\n") }
		words := strings.Split(line, " ")
		num := func(w string) (int, bool) {
			n, err := strconv.Atoi(w)
			return n, err == nil && n >= 0 && !strings.HasPrefix(w, "+")
		}
		switch words[0] {
		case "show", "drops":
			switch {
			case len(words) != 1:
				bad("no argument")
			case words[0] == "drops":
				fmt.Fprintf(&out, "dropped %s\n", strings.Trim(fmt.Sprint(dropped), "[]"))
			default:
				fmt.Fprintf(&out, "frame %d\n", frame)
				for i, p := range panels {
					fmt.Fprintf(&out, "%d %s got=%d seen=%d ", i, p.mode, p.got, p.seen)
					switch p.kind {
					case "chart":
						fmt.Fprintf(&out, "chart n=%d %s\n", len(p.samples), columns(p.samples))
					case "spark":
						fmt.Fprintf(&out, "spark %s\n", columns(p.samples))
					case "gauge":
						fmt.Fprintf(&out, "gauge at=%d to=%d\n", ease(p.from, p.to, frame-p.start), p.to)
					case "strip":
						heat := make([]int, 8)
						for _, x := range p.samples {
							heat[x*8/1000]++
						}
						fmt.Fprintf(&out, "strip %v\n", heat)
					default:
						var ws []string
						for _, e := range p.log {
							ws = append(ws, fmt.Sprintf("%d:%d", e.frame, e.value))
						}
						out.WriteString("log [" + strings.Join(ws, " ") + "]\n")
					}
				}
			}
		case "run", "pause", "resume":
			if len(words) != 2 {
				bad("one number")
				continue
			}
			n, ok := num(words[1])
			if !ok {
				bad("a number from 0")
				continue
			}
			switch {
			case words[0] == "pause" && n < len(panels) && panels[n].mode != "paused":
				panels[n].mode = "paused"
			case words[0] == "resume" && n < len(panels) && panels[n].mode == "paused":
				panels[n].mode = "live"
			case words[0] != "run":
				out.WriteString("? \"" + line + "\": refused\n")
			}
			if words[0] != "run" {
				continue
			}
			for range n {
				frame++
				f := frame
				for i, p := range panels {
					count := 0
					if hash(bank, i, f/180, 1)%4 != 0 {
						h := hash(bank, i, f, 2) % 100
						count = int(h % 3)
						if h < 2 {
							count = 24
						}
					}
					var values []int
					for k := range count {
						wave := ((f+i*37)%200 + 200) % 200
						base := min(wave, 200-wave) * 6
						values = append(values, base+int(hash(bank, i, f, 3+k)%400))
					}
					if len(values) > 16 {
						dropped[i] += len(values) - 16
						if newest[i] {
							values = values[:16]
						} else {
							values = values[len(values)-16:]
						}
					}
					if p.mode == "paused" {
						continue
					}
					if len(values) > 0 {
						switch p.kind {
						case "gauge":
							p.from, p.to, p.start = ease(p.from, p.to, f-p.start), values[len(values)-1], f
						case "log":
							for _, v := range values {
								if v > 900 {
									p.log = append(p.log, entry{f, v})
								}
							}
							p.log = p.log[max(len(p.log)-8, 0):]
						default:
							p.samples = append(p.samples, values...)
							p.samples = p.samples[max(len(p.samples)-p.keep, 0):]
						}
						p.seen, p.got = f, p.got+len(values)
						if p.mode == "stale" {
							p.mode = "live"
							fmt.Fprintf(&out, "%d: %d live\n", f, i)
						}
					}
					if p.mode == "live" && f-p.seen > 60 {
						p.mode = "stale"
						fmt.Fprintf(&out, "%d: %d stale\n", f, i)
					}
				}
			}
		case "policy":
			if len(words) != 3 {
				bad("a panel and oldest or newest")
				continue
			}
			n, ok := num(words[1])
			switch {
			case !ok:
				bad("a number from 0")
			case words[2] != "oldest" && words[2] != "newest":
				bad("oldest or newest")
			case n >= len(panels):
				fmt.Fprintf(&out, "? \"%s\": no panel %d\n", line, n)
			default:
				newest[n] = words[2] == "newest"
			}
		// The scene's commands (dashSceneAsPlay); a play script has none.
		case "echo":
			out.WriteString(strings.TrimPrefix(line, "echo ") + "\n")
		case "toggle":
			n, _ := strconv.Atoi(words[1])
			if panels[n].mode == "paused" {
				panels[n].mode = "live"
			} else {
				panels[n].mode = "paused"
			}
		case "swap":
			n, _ := strconv.Atoi(words[1])
			newest[n] = !newest[n]
		default:
			bad("run, pause, resume, policy, show or drops")
		}
	}
	return out.String()
}
