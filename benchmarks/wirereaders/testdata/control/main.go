// Command control is readerbench's hand Go twin (PORT-WIRE-READERS R1),
// run as `control bench KIND N`: a record's JSON, or a list of four,
// read N times by encoding/json into the record's struct.
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

const one = `{"label":"crate","count":12,"tags":["wood","heavy"],"weight":30.5}`

const four = "[" + one + "," + one + "," + one + "," + one + "]"

//go:noinline
func readOne(text string) (box, bool) {
	var b box
	err := json.Unmarshal([]byte(text), &b)
	return b, err == nil
}

//go:noinline
func readFour(text string) ([]box, bool) {
	var bs []box
	err := json.Unmarshal([]byte(text), &bs)
	return bs, err == nil
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
			bs, ok := readFour(four)
			if !ok {
				fmt.Println(-1)
				return
			}
			for _, b := range bs {
				sum += b.Count
			}
			continue
		}
		b, ok := readOne(one)
		if !ok {
			fmt.Println(-1)
			return
		}
		sum += b.Count
	}
	fmt.Println(sum)
}
