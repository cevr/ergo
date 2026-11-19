// Clock-gui is the MyGo-direct control of cmd/clock-gui (the LIVE
// receipt, stdlib.md UI 24): the same stopwatch and level meter, written
// as a person would on MyGo's own widgets, in Go. A reading sets the
// label's string and asks for a frame, which runs the view again: MyGo
// has no value a frame reads without its view.
//
//	clock-gui                  open the window
//	clock-gui -headless N      publish N readings through ui.Tester and
//	                           print what cmd/clock-gui -headless N prints
//	clock-gui -floor N         the floor: each reading is one atomic store
//	                           and one paint of the two text runs on a
//	                           headless screen.Window, with no view, no
//	                           tree and no layout
package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ergo.local/prototype/hosts/mygo/screen"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// rate is the readings a second.
const rate = 60

// clock is the screen's state: the two readings and the laps.
// The readings are set off the UI thread, so a mutex holds them.
type clock struct {
	mu    sync.Mutex
	time  string
	level string
	laps  int
}

// set sets the two readings.
func (c *clock) set(time, level string) {
	c.mu.Lock()
	c.time, c.level = time, level
	c.mu.Unlock()
}

func (c *clock) view(x *ui.Context) {
	c.mu.Lock()
	ti, le := c.time, c.level
	c.mu.Unlock()
	ui.Column(x).Gap(8).Padding(16).Children(func() {
		ui.Text(x, "time")
		ui.Text(x, ti)
		ui.Text(x, "level")
		ui.Row(x).Gap(8).Children(func() {
			ui.Text(x, le)
			ui.Textf(x, "laps %d", c.laps)
			if ui.Button(x, "Lap").Label("lap").Clicked() {
				c.laps++
			}
		})
	})
}

// stamp is reading i's time, minutes, seconds and hundredths.
func stamp(i int) string {
	cs := i * 100 / rate
	return fmt.Sprintf("%02d:%02d.%02d", cs/6000%100, cs/100%60, cs%100)
}

// level is reading i's level: a saw from 0 to 100 percent.
func level(i int) string { return strconv.Itoa(i%101) + "%" }

func main() {
	if len(os.Args) > 2 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil {
			log.Fatalf("clock-gui: %s takes a count, not %s", os.Args[1], os.Args[2])
		}
		switch os.Args[1] {
		case "-headless":
			headless(n)
			return
		case "-floor":
			floor(n)
			return
		}
	}
	c := &clock{time: stamp(0), level: level(0)}
	w := mygo.NewWindow(mygo.WindowOptions{Title: "Clock", Width: 320, Height: 200, Content: ui.View(c.view)})
	go func() {
		for i := 1; ; i++ {
			time.Sleep(time.Second / rate)
			ti, le := stamp(i), level(i)
			c.set(ti, le)
			w.Invalidate()
		}
	}()
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// headless sets n readings on ui.Tester, a frame each.
func headless(n int) {
	c := &clock{time: stamp(0), level: level(0)}
	tt := ui.NewTester(c.view, 320, 200)
	for i := 1; i <= n; i++ {
		c.set(stamp(i), level(i))
		tt.Frame()
	}
	fmt.Printf("%s %s painted %d\n", c.time, c.level, n+1)
}

// floor stores each reading and paints its two text runs, nothing more.
func floor(n int) {
	w, err := screen.Headless(320, 200)
	if err != nil {
		log.Fatal(err)
	}
	var ti, le atomic.Pointer[string]
	for i := 1; i <= n; i++ {
		s, l := stamp(i), level(i)
		ti.Store(&s)
		le.Store(&l)
		w.Begin()
		w.Text(16, 40, *ti.Load(), 14, 0xff000000)
		w.Text(16, 88, *le.Load(), 14, 0xff000000)
		w.Present()
	}
	fmt.Printf("%s %s painted %d\n", *ti.Load(), *le.Load(), w.Painted())
}
