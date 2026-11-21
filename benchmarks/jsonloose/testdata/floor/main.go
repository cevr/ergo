// Command floor is loosebench's Go-runtime floor (common.md GO-RUNTIME
// FLOOR), run as `floor bench fenced N`: the value found by its first and
// last brace, the least a reader of a value in prose does, then read by
// encoding/json into the record's struct, as the control reads it.
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
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end < start {
		return b, false
	}
	err := json.Unmarshal([]byte(text[start:end+1]), &b)
	return b, err == nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "fenced" {
		fmt.Fprintln(os.Stderr, "usage: floor bench fenced N")
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
