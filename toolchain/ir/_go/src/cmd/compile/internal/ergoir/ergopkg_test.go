package ergoir

import (
	"encoding/binary"
	"strings"
	"testing"
)

func buildTestSpecimen(seed uint32) []byte {
	names := []string{
		"Apply", "Generic", "a/dep", "amd64", "d.Gen", "dep", "go1.27.4",
		"init.0", "init.1", "linux", "main.ergo", "plain", "tiny", "z/tiny",
	}
	ends := make([]uint32, len(names))
	var off uint32
	for i, n := range names {
		off += uint32(len(n))
		ends[i] = off
	}
	colPack := func(width int, rows [][]uint32) []uint32 {
		out := make([]uint32, 0, width*len(rows))
		for c := 0; c < width; c++ {
			for _, row := range rows {
				out = append(out, row[c])
			}
		}
		return out
	}
	ty := colPack(4, [][]uint32{
		{HTBasic, HBasicInt, 0, 0},
		{HTBasic, HBasicAny, 0, 0},
		{HTSignature, 0, 0, 0},
		{HTSignature, 0, 0, 1},
		{HTParameter, 5, 1, 1},
		{HTSignature, 1, 2, 2},
		{HTGeneric, 6, 4, 1},
		{HTSignature, 1, 5, 2},
	})
	sy := colPack(5, [][]uint32{
		{0, 1, HSFunc, 4, 1},
		{0, 5, HSDictionary, 8, 1},
		{0, 8, HSFunc, 3, 2},
		{0, 9, HSFunc, 3, 2},
		{1, 2, HSFunc, 7, 0},
	})
	bs := colPack(5, [][]uint32{
		{1, 0, 2, 0, 3},
		{3, 2, 0, 3, 1},
		{4, 2, 0, 4, 1},
	})
	os := colPack(4, [][]uint32{
		{HOConst, 1, 7, 3},
		{HOCallInstance, 1, 10, 4},
		{HOReturn, 1, 14, 1},
		{HOReturn, 2, 15, 0},
		{HOReturn, 2, 15, 0},
	})
	cols := [][]uint32{
		{14, 13, 10, 4, 7, 12},
		ends,
		{3, 6, 1, 0x04030201, 0x08070605},
		{11, 100, 0, 2, 0x12345678, 0, 0, 0, 0, 0, 0, 0},
		{0, 30},
		colPack(3, [][]uint32{{1, 2, 4}, {1, 40, 43}}),
		ty,
		sy,
		bs,
		{1, 1},
		os,
		{5, 8, 2, 15, 1},
		{},
		{4, 3},
		{1, 2, 5, 5, 5, 1, 1, 1, seed, 0, 1, 1, 2, 1, 2, 1},
	}
	text := strings.Join(names, "")
	var out []byte
	out = append(out, EIRMagic...)
	out = binary.LittleEndian.AppendUint32(out, EIRVersion)
	for i := 0; i < EIRSections-1; i++ {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(cols[i])/eirWidths[i]))
	}
	out = binary.LittleEndian.AppendUint32(out, uint32(len(text)))
	for _, col := range cols {
		for _, v := range col {
			out = binary.LittleEndian.AppendUint32(out, v)
		}
	}
	out = append(out, text...)
	return out
}

func editSpecimenWord(wire []byte, section, row, col int, val uint32) []byte {
	p, err := DecodeEIR(wire)
	if err != nil {
		panic(err)
	}
	cp := append([]byte(nil), wire...)
	at := p.dir[section].start + (p.rows(section)*col+row)*4
	binary.LittleEndian.PutUint32(cp[at:at+4], val)
	return cp
}

func TestDecodeEIRSpecimenAndPositions(t *testing.T) {
	wire := buildTestSpecimen(17)
	p, err := DecodeEIR(wire)
	if err != nil {
		t.Fatalf("DecodeEIR failed: %v", err)
	}
	if p.Target.Path != "z/tiny" || p.PkgName != "tiny" || p.Target.Profile != "plain" {
		t.Fatalf("unexpected target/pkg: %+v %q", p.Target, p.PkgName)
	}
	if len(p.Inits) != 2 || p.Inits[0] != 4 || p.Inits[1] != 3 {
		t.Fatalf("expected non-symbol init order [4, 3], got %v", p.Inits)
	}
	f0, l0, c0 := p.SpanPos(1)
	if f0 != 0 || l0 != 1 || c0 != 3 {
		t.Fatalf("span 1 pos = (%d,%d,%d), want (0,1,3)", f0, l0, c0)
	}
	f1, l1, c1 := p.SpanPos(2)
	if f1 != 0 || l1 != 2 || c1 != 11 {
		t.Fatalf("span 2 pos = (%d,%d,%d), want (0,2,11)", f1, l1, c1)
	}
}

func TestDecodeEIRFailClosed(t *testing.T) {
	wire := buildTestSpecimen(17)

	// Unknown version
	badVer := append([]byte(nil), wire...)
	binary.LittleEndian.PutUint32(badVer[8:12], 99)
	if _, err := DecodeEIR(badVer); err == nil {
		t.Fatal("accepted unknown schema version 99")
	}

	// Bad span end > file size
	badSpan := editSpecimenWord(wire, HSpans, 0, 2, 101)
	if _, err := DecodeEIR(badSpan); err == nil {
		t.Fatal("accepted span past file size")
	}

	// Missing dictionary on instance
	badDict := editSpecimenWord(wire, HInstances, 0, 2, 0)
	if _, err := DecodeEIR(badDict); err == nil {
		t.Fatal("accepted instance without dictionary")
	}

	// Bad slot type in call
	badSlot := editSpecimenWord(wire, HSlots, 0, 0, 2)
	if _, err := DecodeEIR(badSlot); err == nil {
		t.Fatal("accepted mismatched slot type")
	}

	// Duplicate/incomplete init order
	badInit := editSpecimenWord(wire, HInit, 0, 0, 3)
	if _, err := DecodeEIR(badInit); err == nil {
		t.Fatal("accepted duplicate init entry")
	}

	// Truncated file
	if _, err := DecodeEIR(wire[:len(wire)-1]); err == nil {
		t.Fatal("accepted truncated image")
	}
}

func TestAdmitEIRChecksFingerprintAndProfile(t *testing.T) {
	p, err := DecodeEIR(buildTestSpecimen(17))
	if err != nil {
		t.Fatal(err)
	}
	obs := []EIRImport{{
		Path:        "a/dep",
		Name:        "dep",
		HasInit:     true,
		Fingerprint: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
	}}
	if err := AdmitEIR(p, p.Target, obs); err != nil {
		t.Fatalf("valid admission rejected: %v", err)
	}

	stale := obs
	stale[0].Fingerprint[7] ^= 1
	if err := AdmitEIR(p, p.Target, stale); err == nil {
		t.Fatal("accepted stale import fingerprint")
	}

	wrongProfile := p.Target
	wrongProfile.Profile = "race"
	if err := AdmitEIR(p, wrongProfile, obs); err == nil {
		t.Fatal("accepted mismatched flag profile")
	}
}
