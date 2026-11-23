// Command control is readerbench's hand Go twin (PORT-WIRE-READERS R1),
// run as `control bench KIND N`: a record's JSON, or a list of four,
// read N times by encoding/json into the record's struct; KIND map,
// mapstream and mapreplay read a Map of four keys into a map[string]int
// (JSON-CURSOR-ALLOC), the one hand Go read of each.
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

const one = `{"label":"crate","count":12,"tags":["wood","heavy"],"weight":30.5}`

const four = "[" + one + "," + one + "," + one + "," + one + "]"

const counts = `{"crate":12,"barrel":3,"sack":40,"chest":1}`

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

//go:noinline
func readMap(text string) (map[string]int, bool) {
	var m map[string]int
	err := json.Unmarshal([]byte(text), &m)
	return m, err == nil
}

// readValue is counts read as any JSON value, its numbers as json.Number
// (the twin of a json.Value, whose Num keeps its digits).
//
//go:noinline
func readValue(text string) (map[string]any, bool) {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

func main() {
	kinds := map[string]bool{"record": true, "list": true, "map": true, "mapstream": true, "mapreplay": true, "value": true}
	if len(os.Args) != 4 || os.Args[1] != "bench" || !kinds[os.Args[2]] {
		fmt.Fprintln(os.Stderr, "usage: control bench record|list|map|mapstream|mapreplay|value N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	sum := 0
	for i := 0; i < n; i++ {
		switch os.Args[2] {
		case "list":
			bs, ok := readFour(four)
			if !ok {
				fmt.Println(-1)
				return
			}
			for _, b := range bs {
				sum += b.Count
			}
		case "value":
			m, ok := readValue(counts)
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += len(m)
		case "record":
			b, ok := readOne(one)
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += b.Count
		default:
			m, ok := readMap(counts)
			if !ok {
				fmt.Println(-1)
				return
			}
			for _, v := range m {
				sum += v
			}
		}
	}
	fmt.Println(sum)
}
