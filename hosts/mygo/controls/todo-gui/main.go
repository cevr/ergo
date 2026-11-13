// Todo-gui is the MyGo-direct control of examples/todo-gui (corpus
// contract 4): the same list, the same screen and the same labels, written
// as a person would write it on MyGo's own widgets, in Go. It is what the
// Ergo program is measured against: binary size, and the same output.
//
//	todo-gui                 open the window
//	todo-gui -headless < s   play the scene script s on ui.Tester and print
//	                         the list after each step, as the Ergo binaries do
package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// item is one thing to do.
type item struct {
	id   int
	text string
	done bool
}

// list is the model: the items, the next id, the open count kept with them
// and the filter ("all", "open" or "done").
type list struct {
	items []item
	next  int
	left  int
	show  string
	draft string
}

func (l *list) add(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	l.items = append(l.items, item{id: l.next, text: text})
	l.next++
	l.left++
}

func (l *list) flip(id int) {
	for i := range l.items {
		if l.items[i].id == id {
			if l.items[i].done {
				l.left++
			} else {
				l.left--
			}
			l.items[i].done = !l.items[i].done
			return
		}
	}
}

func (l *list) remove(id int) {
	for i, it := range l.items {
		if it.id == id {
			if !it.done {
				l.left--
			}
			l.items = append(l.items[:i:i], l.items[i+1:]...)
			return
		}
	}
}

func (l *list) clearDone() {
	kept := l.items[:0:0]
	for _, it := range l.items {
		if !it.done {
			kept = append(kept, it)
		}
	}
	l.items = kept
}

func (l *list) shown() []item {
	var out []item
	for _, it := range l.items {
		if l.show == "all" || l.show == "open" && !it.done || l.show == "done" && it.done {
			out = append(out, it)
		}
	}
	return out
}

// summary is the line the Ergo binaries print after each step.
func (l *list) summary() string {
	parts := make([]string, len(l.items))
	for i, it := range l.items {
		if it.done {
			parts[i] = "[x] " + it.text
		} else {
			parts[i] = "[ ] " + it.text
		}
	}
	return fmt.Sprintf("%d items, %d left, %s: %s", len(l.items), l.left, l.show, strings.Join(parts, "; "))
}

// view is the screen of examples/todo-gui/view, on MyGo's widgets.
func (l *list) view(c *ui.Context) {
	ui.Column(c).Gap(10).Padding(16).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.TextInput(c, &l.draft).Placeholder("new item").Label("new item").Width(240)
			if ui.Button(c, "Add").Label("add").Clicked() {
				l.add(l.draft)
				l.draft = ""
			}
		})
		ui.Scroll(c).Label("items").Height(360).Gap(4).Children(func() {
			for _, it := range l.shown() {
				ui.Row(c.Key(it.id)).Gap(8).Children(func() {
					done := it.done
					ui.Checkbox(c, &done, it.text)
					if done != it.done {
						l.flip(it.id)
					}
					if ui.Button(c, "×").Label("del " + it.text).Clicked() {
						l.remove(it.id)
					}
				})
			}
		})
		ui.Row(c).Gap(8).Children(func() {
			ui.Textf(c, "%d left", l.left)
			for _, f := range []struct{ label, text string }{{"all", "All"}, {"open", "Open"}, {"done", "Done"}} {
				if ui.Button(c, f.text).Label(f.label).Clicked() {
					l.show = f.label
				}
			}
			if ui.Button(c, "Clear done").Label("clear").Clicked() {
				l.clearDone()
			}
		})
	})
}

func newList() *list { return &list{next: 1, show: "all"} }

// headless plays the script on in through ui.Tester, clicking by label,
// and writes the list after each step to out.
func headless(in io.Reader, out io.Writer) error {
	l := newList()
	tt := ui.NewTester(l.view, 420, 520)
	sc := bufio.NewScanner(in)
	n := 0
	for sc.Scan() {
		raw := sc.Text()
		if t := strings.TrimSpace(raw); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		n++
		line := strings.TrimSuffix(strings.TrimLeft(raw, " \t"), "\r")
		verb, rest, _ := strings.Cut(line, " ")
		var err error
		switch verb {
		case "tap":
			reveal(tt, l, rest)
			err = tt.Click(rest)
		case "type":
			label, text, ok := strings.Cut(rest, "\t")
			if !ok {
				return fmt.Errorf("line %d is no step: %s", n, raw)
			}
			if err = tt.Click(label); err == nil {
				for range len([]rune(l.draft)) {
					tt.Key(0, ui.KeyBackspace)
				}
				tt.Type(text)
			}
		case "settle":
		default:
			return fmt.Errorf("line %d is no step: %s", n, raw)
		}
		if err != nil {
			fmt.Fprintf(out, "%d: no node %s\n", n, strings.SplitN(rest, "\t", 2)[0])
			continue
		}
		tt.Frame()
		fmt.Fprintf(out, "%d: %s\n", n, l.summary())
	}
	return sc.Err()
}

// reveal scrolls the item list until the first row labelled label shows,
// as a person scrolls to a row out of view before tapping it. Find's box
// is clipped to the list, empty while the row is out of view, so reveal
// goes to the top and scrolls down a little at a time.
func reveal(tt *ui.Tester, l *list, label string) {
	inList := false
	for _, it := range l.shown() {
		if label == it.text || label == "del "+it.text {
			inList = true
		}
	}
	if !inList {
		return
	}
	shows := func() bool {
		r, ok := tt.Find(label)
		return ok && r.W > 0 && r.H > 0
	}
	box, ok := tt.Find("items")
	if !ok || shows() {
		return
	}
	x, y := box.X+box.W/2, box.Y+box.H/2
	tt.Scroll(x, y, 0, -1e6)
	tt.Frame()
	for range 400 {
		if shows() {
			return
		}
		tt.Scroll(x, y, 0, 8)
		tt.Frame()
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-headless" {
		w := bufio.NewWriter(os.Stdout)
		err := headless(os.Stdin, w)
		w.Flush()
		if err != nil {
			fmt.Fprintln(os.Stderr, "todo-gui:", err)
			os.Exit(2)
		}
		return
	}
	l := newList()
	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{Title: "To do", Width: 420, Height: 520, Content: ui.View(l.view)})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
