// Command control is kvbench's hand Go twin (benchmarks/kv/cmd/kvbench):
// the same operations over the same keys, as a person writing Go would
// keep a store: a map under a mutex, and for the file store an
// append-only log held open, one quoted line written per put before the
// map changes. Run as `control bench KIND N`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

type store struct {
	mu  sync.Mutex
	m   map[string]string
	log *os.File
}

func (s *store) get(k string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}

func (s *store) put(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.log != nil {
		if _, err := s.log.WriteString("put\t" + strconv.Quote(k) + "\t" + strconv.Quote(v) + "\n"); err != nil {
			return err
		}
	}
	s.m[k] = v
	return nil
}

func keys() []string {
	ks := make([]string, 0, 64)
	for i := range 64 {
		ks = append(ks, fmt.Sprintf("key-%d", i))
	}
	return ks
}

func bench(kind string, n int) (int, error) {
	ks := keys()
	s := &store{m: map[string]string{}}
	switch kind {
	case "get":
		for _, k := range ks {
			_ = s.put(k, "value of "+k)
		}
		found := 0
		for i := range n {
			if v, ok := s.get(ks[i%64]); ok {
				found += len(v)
			}
		}
		return found, nil
	case "fileput":
		f, err := os.OpenFile(filepath.Join(os.TempDir(), "kvbench-go.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		s.log = f
	}
	for i := range n {
		if err := s.put(ks[i%64], "v"); err != nil {
			return 0, err
		}
	}
	return n, nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	sum, err := bench(os.Args[2], n)
	if err != nil {
		fmt.Fprintln(os.Stderr, "control:", err)
		os.Exit(1)
	}
	fmt.Println(sum)
}
