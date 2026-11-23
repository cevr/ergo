// Command floor is the floor for benchmarks/process: the least plain Go
// for each probe, a bare goroutine and a channel of one per run, with no
// recover, no stop and no deadline (page BEAM-RUNTIME (g): "the floor is a
// bare goroutine and a channel"). timed is the same as run: the floor
// arms no timer. It prints what the Ergo probe prints.
package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" || (os.Args[2] != "run" && os.Args[2] != "timed") {
		fmt.Fprintln(os.Stderr, "usage: floor bench run|timed N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	acc := 0
	for i := 0; i < n; i++ {
		res := make(chan int, 1)
		go func() { res <- i & 3 }()
		acc += <-res
	}
	fmt.Println(acc)
}
