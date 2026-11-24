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
	"time"
)

type box struct {
	Label  string   `json:"label"`
	Count  int      `json:"count"`
	Tags   []string `json:"tags"`
	Weight float64  `json:"weight"`
}

type optionalInt struct {
	present bool
	value   int
}

func (o *optionalInt) UnmarshalJSON(b []byte) error {
	n, err := strconv.Atoi(string(b))
	if err == nil {
		*o = optionalInt{present: true, value: n}
	}
	return err
}

type crate struct {
	Count optionalInt `json:"count"`
	Extra optionalInt `json:"extra"`
}

//go:noinline
func readCrate(text string) (crate, bool) {
	var c crate
	err := json.Unmarshal([]byte(text), &c)
	return c, err == nil && c.Count.present
}

//go:noinline
func readWireLeaves() (int, bool) {
	const text = `{"at":"2026-11-23T00:12:00Z","data":{ "x": [1, 2] }}`
	var r struct {
		At   *time.Time      `json:"at"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &r); err != nil || r.At == nil || r.Data == nil {
		return 0, false
	}
	return int(r.At.Unix()%1000) + len(r.Data), true
}

//go:noinline
func readTagged(text string) (int, bool) {
	var e struct {
		Tag   string      `json:"_tag"`
		Count optionalInt `json:"count"`
		Label *string     `json:"label"`
	}
	if err := json.Unmarshal([]byte(text), &e); err != nil || !e.Count.present {
		return 0, false
	}
	switch e.Tag {
	case "Unlabelled":
		return e.Count.value, true
	case "Labelled":
		if e.Label != nil {
			return e.Count.value + len(*e.Label), true
		}
	}
	return 0, false
}

//go:noinline
func readParsed(text string) (int, bool) {
	var r struct {
		Mode  string      `json:"mode"`
		Limit optionalInt `json:"limit"`
	}
	if err := json.Unmarshal([]byte(text), &r); err != nil || !r.Limit.present || r.Limit.value < 0 || r.Limit.value > 99 {
		return 0, false
	}
	switch r.Mode {
	case "low":
		return r.Limit.value, true
	case "high":
		return r.Limit.value + 1, true
	default:
		return 0, false
	}
}

//go:noinline
func readNullable(text string) (int, bool) {
	var r []*int
	if err := json.Unmarshal([]byte(text), &r); err != nil || len(r) != 2 || r[1] == nil {
		return 0, false
	}
	n := *r[1]
	if r[0] != nil {
		n += *r[0]
	}
	return n, true
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
	kinds := map[string]bool{"record": true, "list": true, "map": true, "mapstream": true, "mapreplay": true, "value": true, "omittable": true, "tagged": true, "parsed": true, "nullable": true, "wire-leaves": true, "generic": true}
	if len(os.Args) != 4 || os.Args[1] != "bench" || !kinds[os.Args[2]] {
		fmt.Fprintln(os.Stderr, "usage: control bench record|list|map|mapstream|mapreplay|value|omittable|tagged|parsed|nullable|wire-leaves|generic N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	sum := 0
	for i := 0; i < n; i++ {
		switch os.Args[2] {
		case "wire-leaves":
			got, ok := readWireLeaves()
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += got
		case "nullable":
			text := `[null,12]`
			if i%2 != 0 {
				text = `[3,12]`
			}
			got, ok := readNullable(text)
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += got
		case "parsed":
			text := `{"mode":"low","limit":12}`
			if i%2 != 0 {
				text = `{"limit":3,"mode":"high"}`
			}
			got, ok := readParsed(text)
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += got
		case "tagged":
			text := `{"count":12,"_tag":"Unlabelled"}`
			if i%2 != 0 {
				text = `{"_tag":"Labelled","label":"wood","count":3}`
			}
			got, ok := readTagged(text)
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += got
		case "omittable", "generic":
			text := `{"count":12}`
			if i%2 != 0 {
				text = `{"count":12,"extra":3}`
			}
			c, ok := readCrate(text)
			if !ok {
				fmt.Println(-1)
				return
			}
			sum += c.Count.value
			if c.Extra.present {
				sum += c.Extra.value
			}
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
