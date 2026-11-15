// Package exetest compares two Go executables but for their build IDs:
// the identity check of the direct build path against go build, shared by
// internal/sketch/build's tests and internal/sketchfront's
// TestDirectMatchesGoBuild (which lowers Ergo programs, so it lives with
// the front end and internal/sketch/build's tests stay off the checker).
package exetest

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"os"
	"testing"
)

// buildIDMarker opens the build ID string every Go executable carries
// (cmd/internal/buildid).
const buildIDMarker = "\xff Go build ID: \""

// maskBuildID zeroes every occurrence of the executable's own build ID:
// the .note.go.buildid note of an ELF file, the marked string at the start
// of the text elsewhere. Build IDs differ by design: Go's are its own
// action IDs, the direct path's are its action keys. Nothing else may
// differ.
func maskBuildID(t testing.TB, b []byte) []byte {
	t.Helper()
	var id []byte
	if f, err := elf.NewFile(bytes.NewReader(b)); err == nil {
		s := f.Section(".note.go.buildid")
		if s == nil {
			t.Fatal("ELF executable has no Go build ID note")
		}
		note, err := s.Data()
		// namesz, descsz, type, then the name "Go\x00\x00" and the ID.
		if err != nil || len(note) < 16 {
			t.Fatalf("Go build ID note: %v", err)
		}
		id = bytes.Clone(note[16 : 16+binary.LittleEndian.Uint32(note[4:8])])
		// The linker derives the GNU build ID from the Go build ID.
		if s := f.Section(".note.gnu.build-id"); s != nil {
			gnu, err := s.Data()
			if err != nil || len(gnu) < 16 {
				t.Fatalf("GNU build ID note: %v", err)
			}
			b = bytes.ReplaceAll(b, gnu[16:16+binary.LittleEndian.Uint32(gnu[4:8])], make([]byte, binary.LittleEndian.Uint32(gnu[4:8])))
		}
	} else {
		if f, err := macho.NewFile(bytes.NewReader(b)); err == nil {
			// The linker derives the LC_UUID from the build ID, and the
			// ad hoc code signature hashes every page, the build ID's too.
			b = bytes.Clone(b)
			for _, l := range f.Loads {
				raw := l.Raw()
				switch f.ByteOrder.Uint32(raw[0:4]) {
				case 0x1b: // LC_UUID: cmd, cmdsize, uuid[16]
					b = bytes.ReplaceAll(b, raw[8:24], make([]byte, 16))
				case 0x1d: // LC_CODE_SIGNATURE: cmd, cmdsize, dataoff, datasize
					off, size := f.ByteOrder.Uint32(raw[8:12]), f.ByteOrder.Uint32(raw[12:16])
					clear(b[off : off+size])
				}
			}
		}
		i := bytes.Index(b, []byte(buildIDMarker))
		if i < 0 {
			t.Fatal("executable has no build ID")
		}
		rest := b[i+len(buildIDMarker):]
		j := bytes.Index(rest, []byte("\"\n"))
		if j < 0 {
			t.Fatal("executable has no build ID end")
		}
		id = bytes.Clone(rest[:j])
	}
	return bytes.ReplaceAll(b, id, make([]byte, len(id)))
}

// Same fails unless the two files are equal but for their build IDs,
// naming the first differing offset.
func Same(t testing.TB, what, direct, gobuild string) {
	t.Helper()
	a, err := os.ReadFile(direct)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(gobuild)
	if err != nil {
		t.Fatal(err)
	}
	a, b = maskBuildID(t, a), maskBuildID(t, b)
	if bytes.Equal(a, b) {
		return
	}
	at := 0
	for at < min(len(a), len(b)) && a[at] == b[at] {
		at++
	}
	t.Fatalf("%s: the direct executable (%d bytes) differs from go build's (%d bytes) at offset %d", what, len(a), len(b), at)
}
