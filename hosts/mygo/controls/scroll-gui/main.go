// Scroll-gui is the MyGo-direct control of cmd/scroll-gui (the
// SCROLL-COST receipt, stdlib.md UI 11): the same 10,000-row list,
// written as a person would on MyGo's own virtual List, in Go.
//
//	scroll-gui                  open the window
//	scroll-gui -headless N      scroll N steps of 7 dips through ui.Tester
//	                            and print the views run
//	scroll-gui -floor N         the floor: each step is one offset store
//	                            and one paint of the rows it shows, on a
//	                            headless screen.Window, with no view, no
//	                            tree and no layout
package main

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"ergo.local/prototype/hosts/mygo/screen"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	count  = 10000
	height = 24
)

// views counts the view's runs.
var views int

type rows struct{ list ui.ListState }

func (r *rows) view(c *ui.Context) {
	views++
	ui.Column(c).Children(func() {
		ui.Text(c, "rows")
		ui.List(c, &r.list, count, func(i int) {
			ui.Text(c, "row "+strconv.Itoa(i)).Height(height)
		}).Label("rows").Grow(1)
	})
}

func main() {
	if len(os.Args) > 2 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil {
			log.Fatalf("scroll-gui: %s takes a count, not %s", os.Args[1], os.Args[2])
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
	r := &rows{}
	mygo.NewWindow(mygo.WindowOptions{Title: "Rows", Width: 320, Height: 480, Content: ui.View(r.view)})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// headless scrolls n steps of 7 dips at the list's centre.
func headless(n int) {
	r := &rows{}
	tt := ui.NewTester(r.view, 320, 480)
	for i := 0; i < n; i++ {
		tt.Scroll(160, 240, 0, 7)
	}
	fmt.Printf("views %d\n", views)
}

// floor stores each step's offset and paints the rows it shows.
func floor(n int) {
	w, err := screen.Headless(320, 480)
	if err != nil {
		log.Fatal(err)
	}
	off := 0.0
	for i := 1; i <= n; i++ {
		off = float64(7 * i)
		first := int(off) / height
		w.Begin()
		w.Clip(0, 24, 320, 456)
		for k := first; k < count && float64(k*height)-off < 456; k++ {
			w.Text(0, 24+float64(k*height)-off, "row "+strconv.Itoa(k), 14, 0xff000000)
		}
		w.Unclip()
		w.Present()
	}
	fmt.Printf("offset %g painted %d\n", off, w.Painted())
}
