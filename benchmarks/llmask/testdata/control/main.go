// Command control is askbench's hand Go twin (wf10 L6), run as `control
// bench KIND N`: a model's reply read N times as a person writing Go reads
// one. KIND strict and decode: the text read by encoding/json into the
// record's struct. KIND fenced: the fence cut off with strings.Index and
// the body read by encoding/json.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type box struct {
	Label  string   `json:"label"`
	Count  int      `json:"count"`
	Tags   []string `json:"tags"`
	Weight float64  `json:"weight"`
}

const strict = `{"label":"crate","count":12,"tags":["wood","heavy"],"weight":30.5}`

const fenced = "Sure! Here is the record:\n```json\n" + strict + "\n```\n"

//go:noinline
func read(text string, fence bool) (box, bool) {
	var b box
	body := text
	if fence {
		start := strings.Index(text, "```")
		if start < 0 {
			return b, false
		}
		body = text[start+3:]
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			body = body[nl+1:]
		}
		if end := strings.Index(body, "```"); end >= 0 {
			body = body[:end]
		}
	}
	err := json.Unmarshal([]byte(body), &b)
	return b, err == nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	fence := os.Args[2] == "fenced"
	text := strict
	if fence {
		text = fenced
	}
	sum := 0
	for i := 0; i < n; i++ {
		b, ok := read(text, fence)
		if !ok {
			fmt.Println(-1)
			return
		}
		sum += b.Count
	}
	fmt.Println(sum)
}
