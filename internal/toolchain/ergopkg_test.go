package toolchain

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestErgoPkgRoundTripAndValidation exercises the Route B -ergopkg ingress
// end-to-end against a stock Go dependency (with generics + init), Go-visible
// exported types, non-symbol init order, bidirectional callbacks, runtime
// .ergo source line/file reporting, and fail-closed negative validation.
func TestErgoPkgRoundTripAndValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds the patched compile")
	}
	out, err := exec.Command("go", "env", "GOROOT", "GOVERSION", "GOOS", "GOARCH").Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 4 {
		t.Fatalf("unexpected go env: %q", string(out))
	}
	source, pin, goos, goarch := fields[0], fields[1], fields[2], fields[3]

	c := PatchConfig{Cache: ircDir(t), Source: source}
	if dir := os.Getenv("ERGO_TOOLCHAIN_TEST_IR"); dir != "" {
		c.lock = os.DirFS(dir)
	}
	patched, err := Patch(c)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join("pkg", "tool", hostTools())
	stockCompile := filepath.Join(source, tools, "compile"+exe())
	stockLink := filepath.Join(source, tools, "link"+exe())
	patchedCompile := filepath.Join(patched.Root, tools, "compile"+exe())

	dir := t.TempDir()
	stdOut, err := exec.Command(filepath.Join(source, "bin", "go"+exe()),
		"list", "-export", "-deps", "-f", "{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}",
		"fmt", "os", "path/filepath", "runtime").Output()
	if err != nil {
		t.Fatalf("go list -export: %v", err)
	}
	stdCfg := string(stdOut)
	mustWrite := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mustWrite("std.importcfg", stdCfg)

	depSrc := `package dep

var Trace string

func init() { Trace = "dep" }

func Record(s string) {
	if Trace != "" {
		Trace += ","
	}
	Trace += s
}

func Gen[T any](x T) T { return x }

func ApplyTwice(x int, f func(int) int) int { return f(f(x)) }
`
	depGo := mustWrite("dep.go", depSrc)
	depA := filepath.Join(dir, "dep.a")
	runTool(t, source, stockCompile, dir, "-p", "a/dep", "-importcfg", filepath.Join(dir, "std.importcfg"), "-pack", "-c=1", "-o", depA, depGo)
	depBytes, err := os.ReadFile(depA)
	if err != nil {
		t.Fatal(err)
	}
	depFP := extractArchiveFP(t, depBytes)

	const seed = 17
	k1 := uint32(101) + (uint32(seed)*37)%997
	k2 := uint32(257) + (uint32(seed)*73)%991

	tinyWire, sectionOffs, sectionRows := buildTinyEIRForTest(k1, k2, goos, goarch, pin, depFP)
	tinyEIR := mustWrite("tiny.eir", string(tinyWire))
	tinyCfg := mustWrite("tiny.importcfg", stdCfg+"packagefile a/dep="+depA+"\n")

	tinyErgoA := filepath.Join(dir, "tiny_ergo.a")
	runTool(t, patched.Root, patchedCompile, dir, "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", "-o", tinyErgoA, tinyEIR)

	refSrc := fmt.Sprintf(`package tiny

import "a/dep"

type Pair struct {
	A int
	B int
}

var State int

func init() {
	dep.Record("init1")
	State = 10
}

func init() {
	dep.Record("init0")
	State = State*3 + 7
}

func Bump(x int) int {
	return x + %d
}

func Compute(x int, cb func(int) int) int {
	g := dep.Gen[int](x)
	c := cb(g)
	sum := (c + State) ^ %d
	if sum < 0 {
		sum = sum + %d
	}
	State = sum
	return sum
}

func RoundTripPair(p Pair) Pair {
	return dep.Gen[Pair](p)
}
`, k1, k2, k2)
	refGo := mustWrite("tinyref.go", refSrc)
	tinyRefA := filepath.Join(dir, "tiny_ref.a")
	runTool(t, source, stockCompile, dir, "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", "-o", tinyRefA, refGo)

	mainSrc := fmt.Sprintf(`package main

import (
	"a/dep"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"z/tiny"
)

func main() {
	seed := %d
	mode := os.Args[1]
	fmt.Printf("trace=%%s initState=%%d\n", dep.Trace, tiny.State)
	callerOK := false
	r := tiny.Compute(seed, func(v int) int {
		_, file, line, ok := runtime.Caller(1)
		base := filepath.Base(file)
		if ok && ((mode == "ergo" && base == "main.ergo" && line == 2) || (mode == "stock" && base == "tinyref.go" && line > 1)) {
			callerOK = true
		}
		return v*2 + 5
	})
	fmt.Printf("compute=%%d postState=%%d callerOK=%%v\n", r, tiny.State, callerOK)
	b := dep.ApplyTwice(seed, tiny.Bump)
	p := tiny.RoundTripPair(tiny.Pair{A: seed + 1, B: seed + 2})
	fmt.Printf("twice=%%d pair=(%%d,%%d)\n", b, p.A, p.B)
}
`, seed)
	mainGo := mustWrite("main.go", mainSrc)

	runLinked := func(tag, mode, tinyA string) string {
		cfg := mustWrite(tag+".importcfg", stdCfg+"packagefile a/dep="+depA+"\npackagefile z/tiny="+tinyA+"\n")
		mainO := filepath.Join(dir, "main_"+tag+".o")
		runTool(t, source, stockCompile, dir, "-p", "main", "-importcfg", cfg, "-o", mainO, mainGo)
		bin := filepath.Join(dir, "prog_"+tag+exe())
		runTool(t, source, stockLink, dir, "-importcfg", cfg, "-o", bin, mainO)
		out, err := exec.Command(bin, mode).CombinedOutput()
		if err != nil {
			t.Fatalf("running %s: %v\n%s", tag, err, out)
		}
		return string(out)
	}

	gotOut := runLinked("ergo", "ergo", tinyErgoA)
	wantOut := runLinked("stock", "stock", tinyRefA)
	if gotOut != wantOut {
		t.Fatalf("route B output mismatch:\ngot:\n%swant:\n%s", gotOut, wantOut)
	}
	if !strings.Contains(gotOut, "trace=dep,init1,init0 initState=37") {
		t.Fatalf("unexpected init order output: %s", gotOut)
	}
	if !strings.Contains(gotOut, "callerOK=true") {
		t.Fatalf("expected runtime.Caller to see main.ergo:2, got:\n%s", gotOut)
	}

	mustReject := func(label string, args ...string) {
		t.Helper()
		outA := filepath.Join(dir, "neg_"+label+".a")
		full := append([]string{"-o", outA}, args...)
		cmd := exec.Command(patchedCompile, full...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOROOT="+patched.Root)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("negative check %s unexpectedly succeeded:\n%s", label, out)
		}
		if !strings.Contains(string(out), "ergopkg:") {
			t.Fatalf("negative check %s failed outside ergopkg fail-closed validator:\n%s", label, out)
		}
		if _, err := os.Stat(outA); err == nil {
			t.Fatalf("negative check %s created output file %s", label, outA)
		}
	}

	patchW := func(src []byte, off int, v uint32) []byte {
		cp := bytes.Clone(src)
		binary.LittleEndian.PutUint32(cp[off:off+4], v)
		return cp
	}

	// 1. Unknown schema version
	badVer := mustWrite("bad_ver.eir", string(patchW(tinyWire, 8, 99)))
	mustReject("bad_ver", "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", badVer)

	// 2. Bad span end past file byte size
	spanEndOff := sectionOffs[5] + sectionRows[5]*2*4
	badSpan := mustWrite("bad_span.eir", string(patchW(tinyWire, spanEndOff, 999)))
	mustReject("bad_span", "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", badSpan)

	// 3. Bad non-parameter result slot type ID (type-id-alias)
	badSlot := mustWrite("bad_slot.eir", string(patchW(tinyWire, sectionOffs[9]+8, 2)))
	mustReject("bad_slot", "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", badSlot)

	// 4. Missing generic dictionary (generic-dict-lost)
	instDictOff := sectionOffs[11] + sectionRows[11]*2*4
	badDict := mustWrite("bad_dict.eir", string(patchW(tinyWire, instDictOff, 0)))
	mustReject("bad_dict", "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", badDict)

	// 5. Stale peer import fingerprint (1 bit flipped)
	staleFP := depFP
	staleFP[0] ^= 1
	staleWire, _, _ := buildTinyEIRForTest(k1, k2, goos, goarch, pin, staleFP)
	staleEIR := mustWrite("stale_fp.eir", string(staleWire))
	mustReject("stale_fp", "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", staleEIR)

	// 6. Wrong flag profile (-race vs plain)
	mustReject("race_profile", "-ergopkg", "-race", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", tinyEIR)

	// 7. Mutually exclusive -ergopkg + .go
	mustReject("mix_go", "-ergopkg", "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", refGo)

	// 8. Mutually exclusive -ergopkg + -ergoir
	mustReject("mix_ergoir", "-ergopkg", "-ergoir="+tinyEIR, "-p", "z/tiny", "-importcfg", tinyCfg, "-pack", tinyEIR)
}

func extractArchiveFP(t *testing.T, ar []byte) [8]byte {
	t.Helper()
	if !bytes.HasPrefix(ar, []byte("!<arch>\n")) {
		t.Fatal("missing ar magic")
	}
	at := 8
	for at+60 <= len(ar) {
		name := strings.TrimSpace(string(ar[at : at+16]))
		size, err := strconv.Atoi(strings.TrimSpace(string(ar[at+48 : at+58])))
		if err != nil {
			t.Fatalf("bad ar size: %v", err)
		}
		start := at + 60
		end := start + size
		if name == "__.PKGDEF" || name == "__.PKGDEF/" {
			body := ar[start:end]
			idx := bytes.LastIndex(body, []byte("\n$$\n"))
			if idx < 8 {
				t.Fatal("missing export $$ marker")
			}
			var fp [8]byte
			copy(fp[:], body[idx-8:idx])
			return fp
		}
		at = end
		if at%2 == 1 {
			at++
		}
	}
	t.Fatal("missing __.PKGDEF")
	return [8]byte{}
}

func buildTinyEIRForTest(k1, k2 uint32, goos, goarch, pin string, depFP [8]byte) ([]byte, [16]int, [16]int) {
	names := []string{
		"Bump", "Compute", "Gen", "Pair", "Pair.A", "Pair.B", "Record",
		"RoundTripPair", "State", "a/dep", "d.GenInt", "d.GenPair", "dep",
		goarch, goos, "init.0", "init.1", "init0", "init1", "main.ergo",
		pin, "plain", "tiny", "z/tiny",
	}
	slices.Sort(names)
	names = slices.Compact(names)
	nameID := func(want string) uint32 {
		for i, n := range names {
			if n == want {
				return uint32(i + 1)
			}
		}
		panic("missing name " + want)
	}
	ends := make([]uint32, len(names))
	var off uint32
	for i, n := range names {
		off += uint32(len(n))
		ends[i] = off
	}
	colPack := func(w int, rows [][]uint32) []uint32 {
		out := make([]uint32, 0, w*len(rows))
		for c := 0; c < w; c++ {
			for _, r := range rows {
				out = append(out, r[c])
			}
		}
		return out
	}

	const (
		symBump          = 1
		symCompute       = 2
		symPair          = 3
		symPairA         = 4
		symPairB         = 5
		symRoundTripPair = 6
		symState         = 7
		symDictInt       = 8
		symDictPair      = 9
		symInit0         = 10
		symInit1         = 11
		symDepGen        = 12
		symDepRecord     = 13

		tInt           = 1
		tStr           = 2
		tAny           = 3
		tSigVoid       = 4
		tSigRecord     = 5
		tSigIntToInt   = 6
		tSigCompute    = 7
		tRecordPair    = 8
		tNamedPair     = 9
		tSigPairToPair = 10
		tParamGen      = 11
		tSigGenPoly    = 12
		tGenFunc       = 13
	)

	var operands []uint32
	pushOps := func(xs ...uint32) (uint32, uint32) {
		lo := uint32(len(operands))
		operands = append(operands, xs...)
		return lo, uint32(len(xs))
	}

	var typeRows [][]uint32
	typeRows = append(typeRows, []uint32{1, 2, 0, 0})  // HTBasic int
	typeRows = append(typeRows, []uint32{1, 17, 0, 0}) // HTBasic string
	typeRows = append(typeRows, []uint32{1, 18, 0, 0}) // HTBasic any
	lo, n := pushOps()
	typeRows = append(typeRows, []uint32{3, 0, lo, n})
	lo, n = pushOps(tStr)
	typeRows = append(typeRows, []uint32{3, 1, lo, n})
	lo, n = pushOps(tInt, tInt)
	typeRows = append(typeRows, []uint32{3, 1, lo, n})
	lo, n = pushOps(tInt, tSigIntToInt, tInt)
	typeRows = append(typeRows, []uint32{3, 2, lo, n})
	lo, n = pushOps(symPairA, symPairB)
	typeRows = append(typeRows, []uint32{4, 0, lo, n})
	lo, n = pushOps(tRecordPair)
	typeRows = append(typeRows, []uint32{5, symPair, lo, n})
	lo, n = pushOps(tNamedPair, tNamedPair)
	typeRows = append(typeRows, []uint32{3, 1, lo, n})
	lo, n = pushOps(tAny)
	typeRows = append(typeRows, []uint32{6, symDepGen, lo, n})
	lo, n = pushOps(tParamGen, tParamGen)
	typeRows = append(typeRows, []uint32{3, 1, lo, n})
	lo, n = pushOps(tParamGen)
	typeRows = append(typeRows, []uint32{7, tSigGenPoly, lo, n})

	symRows := [][]uint32{
		{0, nameID("Bump"), 1, tSigIntToInt, 3},
		{0, nameID("Compute"), 1, tSigCompute, 1},
		{0, nameID("Pair"), 3, tNamedPair, 1},
		{0, nameID("Pair.A"), 4, tInt, 1},
		{0, nameID("Pair.B"), 4, tInt, 1},
		{0, nameID("RoundTripPair"), 1, tSigPairToPair, 2},
		{0, nameID("State"), 2, tInt, 1},
		{0, nameID("d.GenInt"), 5, tSigIntToInt, 1},
		{0, nameID("d.GenPair"), 5, tSigPairToPair, 2},
		{0, nameID("init.0"), 1, tSigVoid, 4},
		{0, nameID("init.1"), 1, tSigVoid, 3},
		{1, nameID("Gen"), 1, tGenFunc, 0},
		{1, nameID("Record"), 1, tSigRecord, 0},
	}

	var slots []uint32
	var opRows [][]uint32
	var bodyRows [][]uint32
	addOp := func(code, span uint32, args ...uint32) {
		lo, n := pushOps(args...)
		opRows = append(opRows, []uint32{code, span, lo, n})
	}
	addBody := func(sym uint32, bslots []uint32, emit func()) {
		sl0 := uint32(len(slots))
		slots = append(slots, bslots...)
		op0 := uint32(len(opRows))
		emit()
		bodyRows = append(bodyRows, []uint32{sym, sl0, uint32(len(bslots)), op0, uint32(len(opRows)) - op0})
	}

	addBody(symBump, []uint32{tInt, tInt, tInt}, func() {
		addOp(1, 3, 2, k1, 0)
		addOp(2, 3, 3, 1, 2)
		addOp(15, 3, 3)
	})
	addBody(symCompute, []uint32{tInt, tSigIntToInt, tInt, tInt, tInt, tInt, tInt, tInt}, func() {
		addOp(16, 1, 1, 1, 3, 1)
		addOp(12, 2, 2, 1, 4, 3)
		addOp(9, 1, 5, symState)
		addOp(1, 1, 6, k2, 0)
		addOp(2, 2, 7, 4, 5)
		addOp(5, 2, 7, 7, 6)
		addOp(1, 2, 8, 0, 0)
		addOp(14, 2, 1, 7, 8, 9, 10)
		addOp(2, 2, 7, 7, 6)
		addOp(10, 2, symState, 7)
		addOp(15, 2, 7)
	})
	addBody(symRoundTripPair, []uint32{tNamedPair, tNamedPair}, func() {
		addOp(16, 2, 2, 1, 2, 1)
		addOp(15, 2, 2)
	})
	addBody(symInit0, []uint32{tStr, tInt, tInt, tInt}, func() {
		addOp(1, 4, 1, nameID("init0"), 0)
		addOp(11, 4, symDepRecord, 0, 1)
		addOp(9, 4, 2, symState)
		addOp(1, 4, 3, 3, 0)
		addOp(4, 4, 2, 2, 3)
		addOp(1, 4, 4, 7, 0)
		addOp(2, 4, 2, 2, 4)
		addOp(10, 4, symState, 2)
		addOp(15, 4)
	})
	addBody(symInit1, []uint32{tStr, tInt}, func() {
		addOp(1, 3, 1, nameID("init1"), 0)
		addOp(11, 3, symDepRecord, 0, 1)
		addOp(1, 3, 2, 10, 0)
		addOp(10, 3, symState, 2)
		addOp(15, 3)
	})

	lo1, n1 := pushOps(tInt)
	lo2, n2 := pushOps(tNamedPair)
	instRows := [][]uint32{
		{symDepGen, tSigIntToInt, symDictInt, lo1, n1},
		{symDepGen, tSigPairToPair, symDictPair, lo2, n2},
	}

	widths := [16]int{6, 1, 5, 12, 1, 3, 4, 5, 5, 1, 4, 5, 5, 1, 1, 1}
	cols := [][]uint32{
		{nameID("z/tiny"), nameID("tiny"), nameID(goos), nameID(goarch), nameID(pin), nameID("plain")},
		ends,
		{nameID("a/dep"), nameID("dep"), 1, binary.LittleEndian.Uint32(depFP[0:4]), binary.LittleEndian.Uint32(depFP[4:8])},
		{nameID("main.ergo"), 200, 0, 5, 0x12345678, 0, 0, 0, 0, 0, 0, 0},
		{0, 30, 60, 90, 120},
		colPack(3, [][]uint32{{1, 2, 10}, {1, 34, 42}, {1, 66, 74}, {1, 98, 106}}),
		colPack(4, typeRows),
		colPack(5, symRows),
		colPack(5, bodyRows),
		slots,
		colPack(4, opRows),
		colPack(5, instRows),
		{},
		{symInit1, symInit0},
		operands,
	}
	text := strings.Join(names, "")

	var out []byte
	out = append(out, "ERGOEIR\n"...)
	out = binary.LittleEndian.AppendUint32(out, 1)
	var rows [16]int
	for i := 0; i < 15; i++ {
		rows[i] = len(cols[i]) / widths[i]
		out = binary.LittleEndian.AppendUint32(out, uint32(rows[i]))
	}
	rows[15] = len(text)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(text)))
	var offs [16]int
	for i := 0; i < 15; i++ {
		offs[i] = len(out)
		for _, v := range cols[i] {
			out = binary.LittleEndian.AppendUint32(out, v)
		}
	}
	offs[15] = len(out)
	out = append(out, text...)
	return out, offs, rows
}
