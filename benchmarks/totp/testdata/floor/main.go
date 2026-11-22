// Command floor is the Go floor of totpbench
// (benchmarks/totp/cmd/totpbench): the cheapest Go program for the same
// checks. One keyed HMAC-SHA-1 made once and Reset for each of the
// window's three steps, its sum written into a fixed array, the truncated
// code compared as a number with the code read once from its six digits:
// no allocation per check.
package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"hash"
	"os"
	"strconv"
)

func truncated(m hash.Hash, sum *[20]byte, step int64) uint32 {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m.Reset()
	m.Write(msg[:])
	h := m.Sum(sum[:0])
	off := h[19] & 15
	return (binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff) % 1000000
}

func bench(n int) int {
	m := hmac.New(sha1.New, []byte("12345678901234567890"))
	var sum [20]byte
	codes := make([]string, 1024)
	for k := range 1024 {
		codes[k] = fmt.Sprintf("%06d", truncated(m, &sum, int64(k)))
	}
	found := 0
	for i := range n {
		k := i % 1024
		c := codes[k]
		want := uint32(0)
		for j := 0; j < 6; j++ {
			want = want*10 + uint32(c[j]-'0')
		}
		at := int64(30*k+31) / 30
		ok := 0
		for d := int64(-1); d <= 1; d++ {
			ok |= subtle.ConstantTimeEq(int32(truncated(m, &sum, at+d)), int32(want))
		}
		if ok == 1 {
			found++
		}
	}
	return found
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
