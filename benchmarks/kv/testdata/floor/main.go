// Command floor is the Go floor of kvbench (benchmarks/kv/cmd/kvbench):
// the cheapest Go program for the same operations. A get is one map
// lookup, a put one map store, with no lock (one goroutine); a file put
// is one write of its quoted line, built in a reused buffer, to a log
// held open, then the map store: the same line, reaching the OS at each
// put as the store's does, with no allocation. Run as `floor bench KIND N`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func keys() []string {
	ks := make([]string, 0, 64)
	for i := range 64 {
		ks = append(ks, "key-"+strconv.Itoa(i))
	}
	return ks
}

func bench(kind string, n int) (int, error) {
	ks := keys()
	m := make(map[string]string, 64)
	switch kind {
	case "get":
		for _, k := range ks {
			m[k] = "value of " + k
		}
		found := 0
		for i := range n {
			found += len(m[ks[i%64]])
		}
		return found, nil
	case "fileput":
		f, err := os.OpenFile(filepath.Join(os.TempDir(), "kvbench-floor.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		buf := make([]byte, 0, 64)
		for i := range n {
			k := ks[i%64]
			buf = append(buf[:0], "put\t"...)
			buf = strconv.AppendQuote(buf, k)
			buf = append(buf, '\t')
			buf = strconv.AppendQuote(buf, "v")
			buf = append(buf, '\n')
			if _, err := f.Write(buf); err != nil {
				return 0, err
			}
			m[k] = "v"
		}
		return n, nil
	}
	for i := range n {
		m[ks[i%64]] = "v"
	}
	return n, nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "floor: N is not a count")
		os.Exit(2)
	}
	sum, err := bench(os.Args[2], n)
	if err != nil {
		fmt.Fprintln(os.Stderr, "floor:", err)
		os.Exit(1)
	}
	fmt.Println(sum)
}
