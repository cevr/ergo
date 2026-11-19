// Command control is fk-flow's hand Go control (plan §18.3 FK12): the same
// two scenes, the same paint and the same script host as examples/fk-flow,
// written in Go with no ui package, so scripts/opcount can hold the Ergo
// program's frames to it. It reads the script on standard input and
// prints what fk-flow prints; with -last, only the last screen, as
// `fk-flow -last` does. hosts/mygo/controls/fk-flow holds the same model
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
		fmt.Fprintln(os.Stderr, "fk-flow: the script did not run")
		os.Exit(1)
	}
}
