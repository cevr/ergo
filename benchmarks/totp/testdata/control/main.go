// Command control is totpbench's hand Go twin
// (benchmarks/totp/cmd/totpbench): the same checks, as a person writing
// Go would verify a TOTP code: for each step of the window, the
// HMAC-SHA-1 of the step's eight bytes under a new hmac.New, RFC 4226's
// truncation, the code formatted with leading zeros and compared with
// hmac.Equal. Run as `control bench verify N`.
package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
)

func code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	h := m.Sum(nil)
	off := h[19] & 15
	bin := binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", bin%1000000)
}

func verify(secret []byte, c string, unix int64) (int64, bool) {
	at := unix / 30
	found, ok := int64(0), false
	for d := int64(-1); d <= 1; d++ {
		if hmac.Equal([]byte(c), []byte(code(secret, at+d))) && !ok {
			found, ok = at+d, true
		}
	}
	return found, ok
}

func bench(n int) int {
	secret := []byte("12345678901234567890")
	codes := make([]string, 1024)
	for k := range 1024 {
		codes[k] = code(secret, int64(k))
	}
	found := 0
	for i := range n {
		k := i % 1024
		if _, ok := verify(secret, codes[k], int64(30*k+31)); ok {
			found++
		}
	}
	return found
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench verify N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	fmt.Println(bench(n))
}
