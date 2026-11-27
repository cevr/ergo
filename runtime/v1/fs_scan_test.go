package v1

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"
	"testing"
)

func TestFSScanDirMatchesLstat(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "empty.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 4097)
	for i := range big {
		big[i] = byte('a' + (i % 26))
	}
	bigPath := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(bigPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(bigPath, filepath.Join(dir, "big-hardlink.bin")); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "nested.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("small.txt", filepath.Join(dir, "sym-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(dir, "sym-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("does-not-exist", filepath.Join(dir, "sym-dangling")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := FSScanDir(dir)
	if err != nil {
		t.Fatalf("FSScanDir(%s): %v", dir, err)
	}
	names, err := FSReadDir(dir)
	if err != nil {
		t.Fatalf("FSReadDir(%s): %v", dir, err)
	}
	if len(got) != len(names) {
		t.Fatalf("len(FSScanDir) = %d, len(FSReadDir) = %d", len(got), len(names))
	}
	for i, name := range names {
		if got[i].Name != name {
			t.Fatalf("entry %d Name = %q, want %q", i, got[i].Name, name)
		}
		want, lerr := FSLstatEntry(filepath.Join(dir, name))
		if lerr != nil {
			t.Fatalf("FSLstatEntry(%s): %v", name, lerr)
		}
		if got[i].Entry != want {
			t.Fatalf("entry %q mismatch:\n got  %+v\n want %+v", name, got[i].Entry, want)
		}
	}

	// Empty directory yields empty non-error result.
	emptyDir := t.TempDir()
	emptyGot, err := FSScanDir(emptyDir)
	if err != nil || len(emptyGot) != 0 {
		t.Fatalf("FSScanDir(empty) = (%v, %v), want empty nil", emptyGot, err)
	}
}

func TestFSScanDirManyEntriesChunking(t *testing.T) {
	dir := t.TempDir()
	const count = 600
	wantNames := make([]string, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("entry-%04d-with-long-suffix-to-force-multiple-getdents-chunks", i)
		wantNames = append(wantNames, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(wantNames)

	got, err := FSScanDir(dir)
	if err != nil {
		t.Fatalf("FSScanDir(many): %v", err)
	}
	if len(got) != count {
		t.Fatalf("len(got) = %d, want %d", len(got), count)
	}
	for i, name := range wantNames {
		if got[i].Name != name {
			t.Fatalf("entry %d Name = %q, want %q", i, got[i].Name, name)
		}
		if got[i].Entry.Kind != 0 || got[i].Entry.Size != 1 {
			t.Fatalf("entry %q unexpected metadata %+v", name, got[i].Entry)
		}
	}
}

func TestFSScanDirErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if _, err := FSScanDir(missing); err != (NotFound{Path: missing}) {
		t.Fatalf("FSScanDir(missing) err = %#v, want NotFound", err)
	}

	filePath := filepath.Join(dir, "regular")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FSScanDir(filePath); err == nil {
		t.Fatal("FSScanDir(regular) expected error, got nil")
	} else if _, ok := err.(Failed); !ok {
		t.Fatalf("FSScanDir(regular) err = %#v, want Failed", err)
	}

	if os.Geteuid() != 0 {
		denied := filepath.Join(dir, "secret")
		if err := os.Mkdir(denied, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(denied, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(denied, 0o755)
		if _, err := FSScanDir(denied); err != (Denied{Path: denied}) {
			t.Fatalf("FSScanDir(chmod 000) err = %#v, want Denied", err)
		}
	}
}

func TestFSScanDirIntoReusesBuffers(t *testing.T) {
	d1 := t.TempDir()
	for i := 0; i < 20; i++ {
		if err := os.WriteFile(filepath.Join(d1, fmt.Sprintf("f-%02d", i)), []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d2 := t.TempDir()
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(d2, fmt.Sprintf("g-%02d", i)), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var scratch FSDirScratch
	e1, err := FSScanDirInto(d1, &scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(e1) != 20 {
		t.Fatalf("len(e1) = %d, want 20", len(e1))
	}
	want1 := slices.Clone(e1)

	e2, err := FSScanDirInto(d2, &scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(e2) != 4 {
		t.Fatalf("len(e2) = %d, want 4", len(e2))
	}
	want2 := slices.Clone(e2)

	// Re-scanning d1 agrees with earlier scan and reuses backing slices without reallocation.
	capBefore := cap(scratch.entries)
	var dirBufPtr *byte
	if len(scratch.dirBuf) > 0 {
		dirBufPtr = &scratch.dirBuf[0]
	}

	got1, err := FSScanDirInto(d1, &scratch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got1, want1) {
		t.Fatalf("rescan d1 mismatch:\n got  %+v\n want %+v", got1, want1)
	}
	if cap(scratch.entries) != capBefore {
		t.Fatalf("scratch.entries cap changed from %d to %d", capBefore, cap(scratch.entries))
	}
	if dirBufPtr != nil && &scratch.dirBuf[0] != dirBufPtr {
		t.Fatalf("scratch.dirBuf backing array reallocated")
	}

	allocs := testing.AllocsPerRun(20, func() {
		entries, err := FSScanDirInto(d2, &scratch)
		if err != nil || len(entries) != 4 {
			t.Fatalf("FSScanDirInto(d2): len=%d err=%v", len(entries), err)
		}
	})
	// Only the 4 small entry name strings should allocate (zero scratch buffer allocs).
	if allocs > 6 {
		t.Fatalf("FSScanDirInto(d2) allocated %.1f times per run, want <= 6 (no buffer reallocs)", allocs)
	}

	got2, err := FSScanDirInto(d2, &scratch)
	if err != nil || !reflect.DeepEqual(got2, want2) {
		t.Fatalf("final rescan d2 = (%+v, %v), want %+v", got2, err, want2)
	}
}
