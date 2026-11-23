// Command control is hashbench's hand Go twin (CAP-HASH), run as
// `control bench N`: N writes of one 4 KiB part to one sha256.New, as a
// person writing Go hashes a text read in parts, then its first digest
// byte.
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	part := make([]byte, 4096)
	for i := range part {
		part[i] = byte(i % 256)
	}
	h := sha256.New()
	for i := 0; i < n; i++ {
		h.Write(part)
	}
	fmt.Println(h.Sum(nil)[0])
}
