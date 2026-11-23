// Command control indents one JSON document N times with encoding/json.
// It prints the last result's length and FNV-1a hash.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
)

const source = `[{"id":0,"name":"first","tags":["a","b\"c"],"m":{}},{"id":1,"name":"second","tags":[],"m":{"ok":true}}]` + "\n"

// indent returns an independent string, like Ergo's json.Indent.
func indent(data []byte) (string, error) {
	var out bytes.Buffer
	err := json.Indent(&out, data, "", "  ")
	return out.String(), err
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	data := []byte(source)
	var out string
	for range n {
		out, err = indent(data)
		if err != nil {
			os.Exit(1)
		}
	}
	h := fnv.New64a()
	h.Write([]byte(out))
	fmt.Println(len(out), h.Sum64())
}
