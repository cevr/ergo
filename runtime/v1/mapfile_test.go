package v1

import (
	"bufio"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/runtime/rt"
)

// TestFSMappedKeepsItsInvariant checks Mapped's unsafe.String invariant
// on seeded files: the text is the file's bytes, as FSReadFile gives
// them (the property behind fs.MapFile's doc: its text is ReadFile's); the
// mapping it points at is read only, so no store can change the string;
// it stays mapped after the heap is collected while the Mapped is open;
// and Close unmaps it, once. It also gives FSReadFile's failures for a
// missing path and a directory.
// Mutants (runtime/v1 ledger): a writable mapping (PROT_WRITE) fails the
// protection check; a Close that does not unmap leaves the range mapped;
// an unmap when FSMapFile returns faults the read after the collection.
func TestFSMappedKeepsItsInvariant(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(11, 3))
	sizes := []int{0, 1, 4095, 4096, 3*4096 + 5}
	for range 12 {
		sizes = append(sizes, r.IntN(5*4096))
	}
	for k, size := range sizes {
		path := filepath.Join(dir, strconv.Itoa(k))
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(r.IntN(256))
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		want, err := FSReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m, err := FSMapFile(path)
		if err != nil || m.Text() != want {
			t.Fatalf("FSMapFile(%d bytes): text of %d bytes, %v; want FSReadFile's", size, len(m.Text()), err)
		}
		if size == 0 {
			m.Close()
			continue
		}
		text := m.Text()
		addr := uintptr(unsafe.Pointer(unsafe.StringData(text)))
		if runtime.GOOS == "linux" {
			if perms, ok := mappingPerms(t, addr); !ok || !strings.HasPrefix(perms, "r--") {
				t.Errorf("the mapping of %d bytes has permissions %q; want read only", size, perms)
			}
		}
		data = nil
		runtime.GC()
		runtime.GC()
		if text[size-1] != want[size-1] {
			t.Errorf("the text of %d bytes changed after a collection", size)
		}
		m.Close()
		m.Close()
		if runtime.GOOS == "linux" {
			if perms, ok := mappingPerms(t, addr); ok && perms[3] == 's' {
				t.Errorf("the shared mapping of %d bytes is still mapped after Close (%q)", size, perms)
			}
		}
	}
	missing := filepath.Join(dir, "missing")
	if _, err := FSMapFile(missing); err != (NotFound{Path: missing}) {
		t.Errorf("FSMapFile(missing) error = %#v", err)
	}
	if _, err := FSMapFile(dir); err != (Failed{Path: dir, Reason: "is a directory"}) {
		t.Errorf("FSMapFile(dir) error = %#v", err)
	}
}

// TestMappedTruncationIsADefect truncates a mapped file under a reader:
// the read of a page past the new end faults (SIGBUS), and the reader's
// task, started while the mapping is live, ends with a defect that names
// the file and the byte (rt.MappedFault), not a crash of the process. The
// reader runs on a goroutine of its own, so only its task boundary (rt.Run)
// can have asked for a panic on the fault.
// Mutants (runtime/v1 ledger): a Run that leaves SetPanicOnFault unset
// crashes the test binary; a defect that keeps the runtime's fault value
// names no file.
func TestMappedTruncationIsADefect(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("a truncated mapping faults with SIGBUS on Linux and macOS")
	}
	page := os.Getpagesize()
	path := filepath.Join(t.TempDir(), "readings")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 3*page)), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := FSMapFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error)
	go func() {
		_, err := rt.Run(rt.Root("reader"), func(*rt.Task) (int, error) {
			text := m.Text()
			sum := 0
			for i := 0; i < len(text); i += page {
				sum += int(text[i])
			}
			return sum, nil
		})
		done <- err
	}()
	err = <-done
	var c *rt.Cause
	if !errors.As(err, &c) || len(c.Reasons) != 1 || c.Reasons[0].Kind != rt.KindDefect {
		t.Fatalf("the reader's exit is %v; want one defect", err)
	}
	f, ok := c.Reasons[0].Value.(rt.MappedFault)
	if !ok || f.Path != path || f.Offset != 0 {
		t.Fatalf("the defect is %#v; want a MappedFault of %s at byte 0", c.Reasons[0].Value, path)
	}
	if !strings.Contains(f.Error(), path) {
		t.Errorf("the defect's text %q does not name the file", f.Error())
	}
}

// fsMapLawStatements is the ledger statement of each fs law this file
// runs.
var fsMapLawStatements = map[string]string{
	"fs MapTextIsText": `MapTextIsText(s string) { let m = MapText(s) Clone(m.Text()) == s }`,
}

// TestFSMapLaws runs fs's laws over its bodies on seeded texts.
func TestFSMapLaws(t *testing.T) {
	lawledger.Run(t, map[string]lawledger.Law{
		"fs MapTextIsText": {Statement: fsMapLawStatements["fs MapTextIsText"], Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(5, 8))
			for i := range 1000 {
				b := make([]byte, r.IntN(64))
				for j := range b {
					b[j] = byte(r.IntN(256))
				}
				s := string(b)
				m := FSMapText(s)
				if FSClone(m.Text()) != s {
					t.Fatalf("case %d: MapTextIsText fails on %q", i, s)
				}
				m.Close()
			}
		}},
	})
}

// mappingPerms is the permission field of the line of /proc/self/maps
// whose range holds addr, and whether one does.
func mappingPerms(t *testing.T, addr uintptr) (string, bool) {
	t.Helper()
	f, err := os.Open("/proc/self/maps")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		lo, hi, ok := strings.Cut(fields[0], "-")
		if !ok {
			continue
		}
		start, err1 := strconv.ParseUint(lo, 16, 64)
		end, err2 := strconv.ParseUint(hi, 16, 64)
		if err1 == nil && err2 == nil && uint64(addr) >= start && uint64(addr) < end {
			return fields[1], true
		}
	}
	return "", false
}

// BenchmarkFSMapFile against BenchmarkFSReadFile is the gain that admits
// FSMapFile's unsafe.String: a 64 MiB file read whole, mapped or copied.
// The mapped text is read once to the end, so the page faults count, and
// the Mapped is closed, so the unmap counts too.
func BenchmarkFSMapFile(b *testing.B) {
	benchmarkRead(b, func(path string) (string, func(), error) {
		m, err := FSMapFile(path)
		if err != nil {
			return "", nil, err
		}
		return m.Text(), m.Close, nil
	})
}

// BenchmarkFSReadFile is FSMapFile's control: os.ReadFile and a copy.
func BenchmarkFSReadFile(b *testing.B) {
	benchmarkRead(b, func(path string) (string, func(), error) {
		text, err := FSReadFile(path)
		return text, func() {}, err
	})
}

func benchmarkRead(b *testing.B, read func(string) (string, func(), error)) {
	path := filepath.Join(b.TempDir(), "big")
	if err := os.WriteFile(path, make([]byte, 64<<20), 0o644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(64 << 20)
	for b.Loop() {
		text, done, err := read(path)
		if err != nil {
			b.Fatal(err)
		}
		sum := 0
		for i := 0; i < len(text); i += 4096 {
			sum += int(text[i])
		}
		done()
		if sum != 0 {
			b.Fatal(sum)
		}
	}
}
