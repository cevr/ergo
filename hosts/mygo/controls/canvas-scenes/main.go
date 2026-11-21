// Fk-flow is the MyGo-direct control of examples/canvas-scenes (corpus wave
// FK12c): canvas-scenes's model, paint and script host as
// examples/canvas-scenes/testdata/control writes them in hand Go (model.go,
// copied), and the window on MyGo's own widgets: the scene switch, the
// buttons and the count as MyGo's Button and Text, the canvas a box whose
// Draw replays the scene's paint with MyGo's Painter (a rectangle as Fill,
// a circle as a path of four cubics and FillPath, a trail as StrokePath).
// It is what the Ergo program (examples/canvas-scenes on ui.TestHost) is held
// to and measured against.
//
//	canvas-scenes                      open the window
//	canvas-scenes -headless [-last]    play the script on standard input
//
// Headless, a line goes to the model, as the canvas, the buttons and the
// display's frames would send it, and ui.Tester lays the window out and
// paints it after each frame and act: the frames a person sees. It prints
// what `canvas-scenes` prints on ui.TestHost; with -last, only the last screen.
//
// The window takes the buttons and the display's frames; a press on its
// canvas is not wired, as draw-gui's control's picture is not.
package main

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// window is the program's window over the host h.
type window struct {
	h    *host
	path ui.Path
	t0   float64
}

func (w *window) view(c *ui.Context) {
	p := w.h.p
	ui.Column(c).Children(func() {
		ui.Row(c).Children(func() {
			if ui.Button(c, "Balls").Label("Balls").Clicked() {
				w.act(showBalls)
			}
			if ui.Button(c, "Flow").Label("Flow").Clicked() {
				w.act(showFlow)
			}
		})
		width, height := ballsWidth, ballsHeight
		count := fmt.Sprintf("%d balls", 0)
		clear := "Clear"
		if p.showingFlow {
			ui.Text(c, "Prism Field")
			ui.Text(c, "Move to stir. Click to bloom.")
			width, height = flowWidth, flowHeight
			count = fmt.Sprintf("%d particles", len(p.flow.particles))
			clear = "Reset"
		} else {
			ui.Text(c, "Canvas Art")
			ui.Text(c, "Click the canvas to spawn a ball.")
			count = fmt.Sprintf("%d balls", len(p.balls.balls))
		}
		ui.Box(c).Label("Canvas").Size(float32(width), float32(height)).Draw(w.paint)
		ui.Row(c).Children(func() {
			toggle := "Pause"
			if !p.running() {
				toggle = "Play"
			}
			if ui.Button(c, toggle).Label(toggle).Clicked() {
				w.act(toggled)
			}
			if ui.Button(c, clear).Label(clear).Clicked() {
				w.act(cleared)
			}
			ui.Text(c, count)
		})
		if p.showingFlow {
			ui.Text(c, "Turbulence "+fixed2(p.flow.strength))
			ui.Text(c, "Noise scale "+fixed2(p.flow.scale))
		}
	})
}

func (w *window) act(kind int) {
	w.h.p.signal(kind, 0, 0)
	w.h.paint.paintScene(w.h.p)
}

// paint replays the scene's paint in r. In a window, a running scene asks
// for the next display frame and takes it as a frame of the program.
func (w *window) paint(pt *ui.Painter, r ui.Rect) {
	if w.h.frame == nil && w.h.p.running() {
		now := float64(pt.Now().UnixNano()) / 1000000.0
		if w.t0 == 0 {
			w.t0 = now
		}
		w.h.p.framed(now - w.t0)
		w.h.paint.paintScene(w.h.p)
		pt.AnimationFrame()
	}
	pl := &w.h.paint
	for _, o := range pl.ops {
		c := ui.RGBA(o.color.r, o.color.g, o.color.b, float32(o.color.a)/255)
		pts := pl.pts[o.from:o.to]
		if !o.stroke && o.segs == 5 {
			pt.Fill(ui.Rect{X: r.X + float32(pts[0].x), Y: r.Y + float32(pts[0].y), W: float32(pts[2].x - pts[0].x), H: float32(pts[2].y - pts[0].y)}, c, 0)
			continue
		}
		path := w.path.Reset()
		path.MoveTo(r.X+float32(pts[0].x), r.Y+float32(pts[0].y))
		if o.stroke {
			for _, q := range pts[1:] {
				path.LineTo(r.X+float32(q.x), r.Y+float32(q.y))
			}
			pt.StrokePath(path, float32(o.width), c)
			continue
		}
		for i := 1; i+2 < len(pts); i += 3 {
			path.CubeTo(r.X+float32(pts[i].x), r.Y+float32(pts[i].y), r.X+float32(pts[i+1].x), r.Y+float32(pts[i+1].y), r.X+float32(pts[i+2].x), r.Y+float32(pts[i+2].y))
		}
		path.Close()
		pt.FillPath(path, c)
	}
}

const usage = "usage: canvas-scenes [-headless [-last]]"

func main() {
	headless, last := false, false
	for _, a := range os.Args[1:] {
		switch a {
		case "-headless":
			headless = true
		case "-last":
			last = true
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
	}
	if last && !headless {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if !headless {
		h := &host{p: open(1), period: 16000000}
		h.paint.paintScene(h.p)
		w := &window{h: h}
		mygo.App.WhenReady(func() {
			mygo.NewWindow(mygo.WindowOptions{Title: "canvas-scenes", Width: 1000, Height: 800, Content: ui.View(w.view)})
		})
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	// Headless: the host plays the script on the model, and after each
	// frame and act ui.Tester lays the window out and paints it.
	w := &window{}
	var tt *ui.Tester
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	status := play(os.Stdin, out, last, func(h *host) {
		if tt == nil {
			w.h = h
			tt = ui.NewTester(w.view, 1000, 800)
		}
		tt.Frame()
	})
	out.Flush()
	if status != 0 {
		fmt.Fprintln(os.Stderr, "canvas-scenes: the script did not run")
		os.Exit(1)
	}
}
