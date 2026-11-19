// Command floor is llmbench's Go-runtime floor (common.md GO-RUNTIME
// FLOOR), run as `floor bench KIND N`. KIND fold appends each word of a
// reply straight to one reused buffer: no delta value, no allocation per
// reply after the first. A stream reader that hands each text span to its
// sink does this work and no less. KIND policy is the inner call (the
// script's reply, made once) and one compare per wrapper: a policy around
// a model that answers costs no less. KIND replay is one pass over the
// line's bytes, the text spans counted (wf10 L3): a reader of the line
// does this work and no less.
package main

import (
	"fmt"
	"os"
	"strconv"
)

const part = 64

var words = []string{"the ", "model ", "said ", "a ", "few ", "short ", "words ", "now "}

type turn struct {
	text          string
	input, output int
}

//go:noinline
func inner(replies []turn, assistants int) (turn, bool) {
	if assistants >= len(replies) {
		return turn{}, false
	}
	return replies[assistants], true
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	t := 0
	var b []byte
	if os.Args[2] == "replay" {
		var line []byte
		line = append(line, `{"Digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","Deltas":[`...)
		for i := 0; i < 8; i++ {
			line = append(line, `{"_tag":"Text","Text":"`...)
			line = append(line, words[i]...)
			line = append(line, `"},`...)
		}
		line = append(line, `],"End":{"_tag":"Answered"}}`...)
		for i := 0; i < n; i++ {
			quotes := 0
			for _, c := range line {
				if c == '"' {
					quotes++
				}
			}
			t += quotes & 1
		}
	}
	if os.Args[2] == "policy" {
		replies := []turn{{text: "ok", input: 1, output: 1}}
		used := 0
		for i := 0; i < n; i++ {
			t1, ok := inner(replies, 0)
			if !ok { // fallback: a next member
				os.Exit(1)
			}
			if t1.text == "" && !ok { // retry: the class of a failure
				os.Exit(1)
			}
			used += t1.output // meter
			t += len(t1.text)
		}
		t += used
	}
	if os.Args[2] == "fold" {
		for r := 0; r < n/part; r++ {
			b = b[:0]
			for i := 0; i < part; i++ {
				b = append(b, words[(r+i)&7]...)
			}
			t += len(b)
		}
	}
	fmt.Println(t)
}
