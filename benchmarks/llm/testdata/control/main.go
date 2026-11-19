// Command control is llmbench's hand Go twin (benchmarks/llm/cmd/llmbench),
// run as `control bench KIND N`. KIND fold makes each reply's 64 text
// deltas as a person writing Go models a stream part (a kind and its
// fields in one struct, the slice made to size), then folds them with a
// switch that appends each text to one buffer.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const part = 64

var words = []string{"the ", "model ", "said ", "a ", "few ", "short ", "words ", "now "}

type kind uint8

const (
	text kind = iota
	reasoning
	callStart
	callArgs
	callEnd
)

// delta is one stream part: its kind, the call's index, id and name, and
// its text.
type delta struct {
	kind     kind
	index    int
	id, name string
	text     string
}

func reply(r int) []delta {
	ds := make([]delta, 0, part)
	for i := 0; i < part; i++ {
		ds = append(ds, delta{kind: text, text: words[(r+i)&7]})
	}
	return ds
}

// fold is the text of the deltas (the other kinds are skipped: the
// workload streams text only).
func fold(ds []delta) string {
	var b []byte
	for i := range ds {
		switch ds[i].kind {
		case text:
			b = append(b, ds[i].text...)
		}
	}
	return string(b)
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	t := 0
	if os.Args[2] == "fold" {
		for r := 0; r < n/part; r++ {
			t += len(fold(reply(r)))
		}
	}
	fmt.Println(t)
}
