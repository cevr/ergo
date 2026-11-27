//go:build linux && (amd64 || arm64)

package v1

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"unsafe"
)

func TestLinuxStatxStructLayoutAndFallback(t *testing.T) {
	if got := unsafe.Sizeof(linuxStatx{}); got != 256 {
		t.Fatalf("sizeof(linuxStatx) = %d, want 256", got)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Nlink); off != 0x10 {
		t.Fatalf("offsetof(linuxStatx.Nlink) = %#x, want 0x10", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Mode); off != 0x1c {
		t.Fatalf("offsetof(linuxStatx.Mode) = %#x, want 0x1c", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Ino); off != 0x20 {
		t.Fatalf("offsetof(linuxStatx.Ino) = %#x, want 0x20", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Size); off != 0x28 {
		t.Fatalf("offsetof(linuxStatx.Size) = %#x, want 0x28", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Blocks); off != 0x30 {
		t.Fatalf("offsetof(linuxStatx.Blocks) = %#x, want 0x30", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Atime); off != 0x40 {
		t.Fatalf("offsetof(linuxStatx.Atime) = %#x, want 0x40", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.Mtime); off != 0x70 {
		t.Fatalf("offsetof(linuxStatx.Mtime) = %#x, want 0x70", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.DevMajor); off != 0x88 {
		t.Fatalf("offsetof(linuxStatx.DevMajor) = %#x, want 0x88", off)
	}
	if off := unsafe.Offsetof(linuxStatx{}.DevMinor); off != 0x8c {
		t.Fatalf("offsetof(linuxStatx.DevMinor) = %#x, want 0x8c", off)
	}

	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(a, []byte("abcde"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, filepath.Join(dir, "a-link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(dir, "sym")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sStatx, sFstatat, sPortable FSDirScratch
	gotStatx, err := scanDirLinux(dir, &sStatx, false)
	if err != nil {
		t.Fatalf("scanDirLinux(statx): %v", err)
	}
	gotFstatat, err := scanDirLinux(dir, &sFstatat, true)
	if err != nil {
		t.Fatalf("scanDirLinux(fstatat): %v", err)
	}
	gotPortable, err := readDirLstatFallback(dir, &sPortable)
	if err != nil {
		t.Fatalf("readDirLstatFallback: %v", err)
	}

	if !reflect.DeepEqual(gotStatx, gotFstatat) {
		t.Fatalf("statx vs fstatat mismatch:\n statx    = %+v\n fstatat  = %+v", gotStatx, gotFstatat)
	}
	if !reflect.DeepEqual(gotStatx, gotPortable) {
		t.Fatalf("statx vs portable fallback mismatch:\n statx    = %+v\n portable = %+v", gotStatx, gotPortable)
	}
}
