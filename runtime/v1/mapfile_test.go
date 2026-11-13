package v1

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unsafe"
)

// TestFSMapFileKeepsItsInvariant checks FSMapFile's unsafe.String
// invariant: the text is the file's bytes, the mapping it points at is
// read-only (no store can change the string), and it stays mapped after
// every reference but the string is gone and the heap is collected. It
// also gives FSReadFile's failures for a missing path and a directory.
// Mutants (runtime/v1 ledger): a writable mapping (PROT_WRITE) fails the
// protection check; an unmap when the body returns faults the read after
// the collection.
func TestFSMapFileKeepsItsInvariant(t *testing.T) {
	dir := t.TempDir()
	sizes := []int{0, 1, 4095, 4096, 3*4096 + 5}
	for _, size := range sizes {
		path := filepath.Join(dir, strconv.Itoa(size))
		data := make([]byte, size)
		for i := range data {
			data[i] = byte('a' + i%23)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		text, err := FSMapFile(path)
		if err != nil || text != string(data) {
			t.Fatalf("FSMapFile(%d bytes) = %d bytes, %v; want the file", size, len(text), err)
		}
		if size == 0 {
			continue
		}
		if runtime.GOOS == "linux" {
			if perms := mappingPerms(t, uintptr(unsafe.Pointer(unsafe.StringData(text)))); !strings.HasPrefix(perms, "r--") {
				t.Errorf("the mapping of %d bytes has permissions %q; want read only", size, perms)
			}
		}
		data = nil
		runtime.GC()
		runtime.GC()
		if text[size-1] != byte('a'+(size-1)%23) {
			t.Errorf("the text of %d bytes changed after a collection", size)
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

// mappingPerms is the permission field of the line of /proc/self/maps
// whose range holds addr.
func mappingPerms(t *testing.T, addr uintptr) string {
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
			return fields[1]
		}
	}
	t.Fatalf("no mapping holds %#x", addr)
	return ""
}

// BenchmarkFSMapFile against BenchmarkFSReadFile is the gain that admits
// FSMapFile's unsafe.String: a 64 MiB file read whole, mapped or copied.
// The mapped text is read once to the end, so the page faults count.
func BenchmarkFSMapFile(b *testing.B) { benchmarkRead(b, FSMapFile) }

// BenchmarkFSReadFile is FSMapFile's control: os.ReadFile and a copy.
func BenchmarkFSReadFile(b *testing.B) { benchmarkRead(b, FSReadFile) }

func benchmarkRead(b *testing.B, read func(string) (string, error)) {
	path := filepath.Join(b.TempDir(), "big")
	if err := os.WriteFile(path, make([]byte, 64<<20), 0o644); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(64 << 20)
	for b.Loop() {
		text, err := read(path)
		if err != nil {
			b.Fatal(err)
		}
		sum := 0
		for i := 0; i < len(text); i += 4096 {
			sum += int(text[i])
		}
		if sum != 0 {
			b.Fatal(sum)
		}
	}
}
