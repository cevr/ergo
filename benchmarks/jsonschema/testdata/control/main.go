// Command control is schemabench's hand Go twin (wf10 JS0), run as
// `control bench conforms N`: the value decoded once by encoding/json into
// map[string]any, then checked N times by a walk written by hand for the
// one schema schemabench checks (Box: label, an optional note, an integer
// count, a tagged size, a list of tags), as a person writing Go checks a
// tool's arguments without a schema library.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
)

const box = `{"label":"crate","count":12,"size":{"_tag":"Large","inches":30},"tags":["wood","heavy"]}`

func integer(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f)
}

func str(v any) bool {
	_, ok := v.(string)
	return ok
}

//go:noinline
func fits(v any) bool {
	o, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, k := range []string{"label", "count", "size", "tags"} {
		if _, ok := o[k]; !ok {
			return false
		}
	}
	if !str(o["label"]) || !integer(o["count"]) {
		return false
	}
	if n, ok := o["note"]; ok && !str(n) {
		return false
	}
	size, ok := o["size"].(map[string]any)
	if !ok {
		return false
	}
	switch size["_tag"] {
	case "Small":
	case "Large":
		in, ok := size["inches"]
		if !ok || !integer(in) {
			return false
		}
	default:
		return false
	}
	tags, ok := o["tags"].([]any)
	if !ok {
		return false
	}
	for _, t := range tags {
		if !str(t) {
			return false
		}
	}
	return true
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || os.Args[2] != "conforms" {
		fmt.Fprintln(os.Stderr, "usage: control bench conforms N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	var v any
	if err := json.Unmarshal([]byte(box), &v); err != nil {
		os.Exit(1)
	}
	fit := 0
	for i := 0; i < n; i++ {
		if fits(v) {
			fit++
		}
	}
	fmt.Println(fit)
}
