// Command control is loosebench's hand Go twin (wf10 JL), run as
// `control bench fenced N`: a model's reply read N times as a person
// writing Go reads one, the fence cut off with strings.Index and the body
// read by encoding/json into the record's struct.
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

const fenced = "Sure! Here is the record:\n```json\n" + `{"label":"crate","count":12,"tags":["wood","heavy"],"weight":30.5}` + "\n```\n"

//go:noinline
func read(text string) (box, bool) {
	var b box
	start := strings.Index(text, "```")
	if start < 0 {
		return b, false
	}
	body := text[start+3:]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	}
	if end := strings.Index(body, "```"); end >= 0 {
		body = body[:end]
	}
	err := json.Unmarshal([]byte(body), &b)
	return b, err == nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "fenced" {
		fmt.Fprintln(os.Stderr, "usage: control bench fenced N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	sum := 0
	for i := 0; i < n; i++ {
		b, ok := read(fenced)
		if !ok {
			fmt.Println(-1)
			return
		}
		sum += b.Count
	}
	fmt.Println(sum)
}
