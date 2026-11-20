// Command control is canvas-scenes's hand Go control (plan §18.3 FK12): the same
// two scenes, the same paint and the same script host as examples/canvas-scenes,
// written in Go with no ui package, so scripts/opcount can hold the Ergo
// program's frames to it. It reads the script on standard input and
// prints what canvas-scenes prints; with -last, only the last screen, as
// `canvas-scenes -last` does. hosts/mygo/controls/canvas-scenes holds the same model
// (model.go, copied) on MyGo's widgets.
package main

import (
	"bufio"
	"fmt"
	"os"
)

const usage = "usage: control [-last] < script"

func main() {
	last := false
	for _, a := range os.Args[1:] {
		if a != "-last" {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		last = true
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	status := play(os.Stdin, out, last, nil)
	out.Flush()
	if status != 0 {
		fmt.Fprintln(os.Stderr, "canvas-scenes: the script did not run")
		os.Exit(1)
	}
}
