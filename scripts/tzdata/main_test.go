package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestZonesBinIsEncoded holds the checked-in runtime/tzdata/zones.bin to
// Encode: read back by the format the package comment states, its zones
// encode, under its own header's release and digest, to its bytes. It does
// not compare with the toolchain's zoneinfo.zip (-check does; a toolchain
// of another release would fail it without a change here). Mutant
// t2-tzdata-name-length-byte (Encode writes the name length in one byte)
// gives other bytes.
func TestZonesBinIsEncoded(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "runtime", "tzdata", "zones.bin"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitN(data, []byte("\n"), 4)
	if len(lines) < 4 || string(lines[0]) != "ergo-tz 1" || !bytes.HasPrefix(lines[1], []byte("iana ")) || !bytes.HasPrefix(lines[2], []byte("source ")) {
		t.Fatalf("zones.bin header %q", Header(data))
	}
	release := strings.TrimPrefix(string(lines[1]), "iana ")
	digest := strings.TrimPrefix(string(lines[2]), "source ")
	var zones []Zone
	for rest := lines[3]; len(rest) > 0; {
		if len(rest) < 2 {
			t.Fatalf("zones.bin ends inside a name length after %d zones", len(zones))
		}
		n := int(binary.BigEndian.Uint16(rest))
		if len(rest) < 2+n+4 {
			t.Fatalf("zones.bin ends inside zone %d", len(zones))
		}
		name := string(rest[2 : 2+n])
		size := int(binary.BigEndian.Uint32(rest[2+n:]))
		rest = rest[2+n+4:]
		if len(rest) < size {
			t.Fatalf("zones.bin ends inside the data of %s", name)
		}
		zones = append(zones, Zone{Name: name, Data: rest[:size]})
		rest = rest[size:]
	}
	again, err := Encode(release, digest, zones)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Errorf("Encode of zones.bin's %d zones is %d bytes, not zones.bin's %d", len(zones), len(again), len(data))
	}
}
