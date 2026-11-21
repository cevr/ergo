// Command floor is the Go floor of sessionbench
// (benchmarks/session/cmd/sessionbench): the cheapest Go program for the
// same checks. One SHA-256 of the token into a fixed array, one map read
// of the end the session is valid until (the earlier of its maximum age
// and its idle end, kept as int64 nanoseconds), no lock (one goroutine),
// no allocation per check.
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"unsafe"
)

func bench(n int) int {
	now := int64(1000) * 1e9
	until := map[[32]byte]int64{}
	tokens := make([]string, 0, 1024)
	for i := range 1024 {
		t := fmt.Sprintf("token %d of the receipt, 32 bytes or more", i)
		until[sha256.Sum256([]byte(t))] = now + int64(3600)*1e9
		tokens = append(tokens, t)
	}
	valid := 0
	for i := range n {
		t := tokens[i%1024]
		d := sha256.Sum256(unsafe.Slice(unsafe.StringData(t), len(t)))
		if u, ok := until[d]; ok && now < u {
			valid++
		}
	}
	return valid
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench verify N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "floor: N is not a count")
		os.Exit(2)
	}
	fmt.Println(bench(n))
}
