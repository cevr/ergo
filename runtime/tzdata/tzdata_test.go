package tzdata

import (
	"encoding/binary"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestBigEndianByHand: be16 and be32 read what encoding/binary.BigEndian
// reads (the reference; this test may import it, the package may not),
// on the boundary values (0, 1, each byte's high bit, the maximum) at the
// start, the middle and the end of a string, and on seeded random bytes.
// ERGO_SEED=<n> replays a seed.
//
// Mutants: tzdata-be16-little-endian, tzdata-be32-drops-high-byte.
func TestBigEndianByHand(t *testing.T) {
	check := func(why string, s string, at int) {
		t.Helper()
		b := []byte(s)
		if at+2 <= len(b) {
			if got, want := be16(s, at), int(binary.BigEndian.Uint16(b[at:])); got != want {
				t.Fatalf("%s: be16(% x, %d) = %d, binary.BigEndian %d", why, b, at, got, want)
			}
		}
		if at+4 <= len(b) {
			if got, want := be32(s, at), int(binary.BigEndian.Uint32(b[at:])); got != want {
				t.Fatalf("%s: be32(% x, %d) = %d, binary.BigEndian %d", why, b, at, got, want)
			}
		}
	}
	bounds := []uint32{0, 1, 0x7f, 0x80, 0xff, 0x100, 0x7fff, 0x8000, 0xffff, 0x10000, 0x7fffffff, 0x80000000, 0xffffffff, 0x01020304}
	for _, v := range bounds {
		var w [4]byte
		binary.BigEndian.PutUint32(w[:], v)
		for _, pad := range []string{"", "x", "\xff\x00\x80"} {
			s := pad + string(w[:]) + pad
			check("bound "+strconv.FormatUint(uint64(v), 16), s, len(pad))
			check("bound low half "+strconv.FormatUint(uint64(v), 16), s, len(pad)+2)
		}
	}
	seed := uint64(1)
	if v := os.Getenv("ERGO_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewPCG(seed, 0))
	for c := range 2000 {
		b := make([]byte, 4+r.IntN(8))
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		why := "case " + strconv.Itoa(c) + " of seed " + strconv.FormatUint(seed, 10) + " (replay: ERGO_SEED=" + strconv.FormatUint(seed, 10) + " go test -run TestBigEndianByHand ./runtime/tzdata)"
		check(why, string(b), r.IntN(len(b)-3))
	}
}

// TestCopyReadsWhole: the copy's index, read through be16 and be32,
// reaches every zone of the shipped zones.bin, each a TZif file, and ends
// exactly at the copy's end: a wrong length would misplace every later
// name or overrun. It is the decode on the real data, which the std tz
// laws read through Zone.
func TestCopyReadsWhole(t *testing.T) {
	names := Names()
	if len(names) < 300 {
		t.Fatalf("the copy holds %d zones; the IANA database has about 600", len(names))
	}
	end := 0
	for _, n := range names {
		z := Zone(n)
		if !strings.HasPrefix(z, "TZif") {
			t.Fatalf("zone %q does not start with TZif (%q)", n, z[:min(len(z), 8)])
		}
		e := index().at[n]
		end = max(end, e.at+e.n)
	}
	if end != len(zones) {
		t.Fatalf("the last zone ends at %d, the copy at %d", end, len(zones))
	}
	for _, n := range []string{"UTC", "America/Toronto", "Europe/London"} {
		if Zone(n) == "" {
			t.Errorf("the copy has no %s", n)
		}
	}
}
