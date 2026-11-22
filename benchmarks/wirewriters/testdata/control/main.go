// Command control is writerbench's hand Go twin (W1), run as
// `control bench KIND N`: a record, or a list of four, written as JSON N
// times by encoding/json's Marshal.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type box struct {
	Label  string   `json:"label"`
	Count  int      `json:"count"`
	Tags   []string `json:"tags"`
	Weight float64  `json:"weight"`
}

var one = box{Label: "crate", Count: 12, Tags: []string{"wood", "heavy"}, Weight: 30.5}

var four = []box{one, one, one, one}

//go:noinline
func writeOne(b box) int {
	text, err := json.Marshal(b)
	if err != nil {
		return -1
	}
	return len(text)
}

//go:noinline
func writeFour(bs []box) int {
	text, err := json.Marshal(bs)
	if err != nil {
		return -1
	}
	return len(text)
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || (os.Args[2] != "record" && os.Args[2] != "list") {
		fmt.Fprintln(os.Stderr, "usage: control bench record|list N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	sum := 0
	for i := 0; i < n; i++ {
		if os.Args[2] == "list" {
			sum += writeFour(four)
			continue
		}
		sum += writeOne(one)
	}
	fmt.Println(sum)
}
