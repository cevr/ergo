// Command control is the hand Go twin of benchmarks/jsonindent: the same
// compact text of N records, indented by encoding/json's Indent; it prints
// what `indentbench bench N` prints, the output's length and FNV-1a hash.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
)

func source(n int) []byte {
	var b bytes.Buffer
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "{\"id\":%d,\"name\":\"n%d\",\"tags\":[\"a\",\"b\\\"c\"],\"m\":{}}", i, i)
	}
	b.WriteString("]")
	return b.Bytes()
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
	var out bytes.Buffer
	if err := json.Indent(&out, source(n), "", "  "); err != nil {
		os.Exit(1)
	}
	h := fnv.New64a()
	h.Write(out.Bytes())
	fmt.Println(out.Len(), h.Sum64())
}
