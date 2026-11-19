// Command control is llmbench's hand Go twin (benchmarks/llm/cmd/llmbench),
// run as `control bench KIND N`. KIND fold makes each reply's 64 text
// deltas as a person writing Go models a stream part (a kind and its
// fields in one struct, the slice made to size), then folds them with a
// switch that appends each text to one buffer. KIND policy asks a script
// N times as a person writing Go wraps a model call: a fallback loop over
// the members, a retry loop with a switch on the error's class, and a
// usage sum (wf10 L2). KIND replay reads a recorded exchange's JSON line
// with encoding/json, compares the request's SHA-256 digest and joins the
// deltas' text (wf10 L3).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
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

// The policy kind: one request, one scripted reply, the three wrappers.

type message struct {
	assistant bool
	text      string
}

type usage struct{ input, output int }

type turn struct {
	text  string
	usage usage
}

// class is how a failed ask may be asked again.
type class uint8

const (
	safe class = iota
	unknown
	unsafe
)

type failure struct {
	class class
	after time.Duration
}

func (f *failure) Error() string { return "failed" }

// script is the scripted model: the reply numbered by the assistant
// messages, its deltas folded into the turn's text.
func script(replies [][]delta, ms []message) (turn, error) {
	n := 0
	for i := range ms {
		if ms[i].assistant {
			n++
		}
	}
	if n >= len(replies) {
		return turn{}, &failure{class: unknown}
	}
	return turn{text: fold(replies[n]), usage: usage{1, 1}}, nil
}

// ask is the fallback over members inside a retry of up to retries more
// asks, its usage added to used.
func ask(members [][][]delta, ms []message, retries int, used *usage) (turn, error) {
	for run := 0; ; run++ {
		var err error
		for _, m := range members {
			var t turn
			t, err = script(m, ms)
			if err == nil {
				used.input += t.usage.input
				used.output += t.usage.output
				return t, nil
			}
		}
		var f *failure
		if !errors.As(err, &f) || f.class == unsafe || run == retries {
			return turn{}, err
		}
		time.Sleep(f.after)
	}
}

// The replay kind: one exchange as its line holds it.

type exchange struct {
	Digest string `json:"Digest"`
	Deltas []struct {
		Tag  string `json:"_tag"`
		Text string `json:"Text"`
	} `json:"Deltas"`
	End struct {
		Tag  string `json:"_tag"`
		Turn struct {
			Text string `json:"Text"`
		} `json:"Turn"`
	} `json:"End"`
}

// digest is the request's name in a recording: SHA-256 of its form.
func digest(form string) string {
	d := sha256.Sum256([]byte(form))
	return hex.EncodeToString(d[:])
}

func replay(line []byte, form string) (string, bool) {
	var x exchange
	if err := json.Unmarshal(line, &x); err != nil || x.Digest != digest(form) {
		return "", false
	}
	var b []byte
	for i := range x.Deltas {
		if x.Deltas[i].Tag == "Text" {
			b = append(b, x.Deltas[i].Text...)
		}
	}
	return string(b), true
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
	if os.Args[2] == "replay" {
		form := "r0:1;u1:q0;AP0;"
		x := exchange{Digest: digest(form)}
		for i := 0; i < 8; i++ {
			x.Deltas = append(x.Deltas, struct {
				Tag  string `json:"_tag"`
				Text string `json:"Text"`
			}{"Text", words[i]})
		}
		x.End.Tag = "Answered"
		line, _ := json.Marshal(x)
		for i := 0; i < n; i++ {
			text, ok := replay(line, form)
			if !ok {
				os.Exit(1)
			}
			t += len(text)
		}
	}
	if os.Args[2] == "policy" {
		members := [][][]delta{{{{kind: text, text: "ok"}}}}
		ms := []message{{text: "q"}}
		var used usage
		for i := 0; i < n; i++ {
			t1, err := ask(members, ms, 3, &used)
			if err != nil {
				os.Exit(1)
			}
			t += len(t1.text)
		}
		t += used.output
	}
	if os.Args[2] == "fold" {
		for r := 0; r < n/part; r++ {
			t += len(fold(reply(r)))
		}
	}
	fmt.Println(t)
}
