package producer

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// verifies reports only whether image verifies against cdhash.
func verifies(image, cdhash []byte) error {
	_, err := verifyCodeSignature(image, cdhash)
	return err
}

// darwinIdentity acquires an identity as Darwin does once the kernel has
// answered cdhash and the file at the executable path held image.
func darwinIdentity(image, cdhash []byte) Identity {
	open := func() (io.ReadCloser, error) { return authenticatedCode(image, cdhash) }
	info := func() (*debug.BuildInfo, bool) { return nil, false }
	nonce := func() (string, error) { return "nonce", nil }
	return acquireOnce(open, info, nonce, nil)()
}

// codeDigest is the Darwin rule written out: SHA-256 over the domain tag and
// the bytes below the signature.
func codeDigest(image []byte, codeEnd int) string {
	sum := sha256.Sum256(append([]byte(darwinCodeTag), image[:codeEnd]...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestDarwinIdentityIsTheAuthenticatedCode: on Darwin the identity is the
// code the cdhash authenticates, [0, codeLimit), under its own domain tag.
// Files with the same cdhash and different signature-container or trailing
// bytes run the same code, so they get the same artifact identity; a file
// with different code gets a process-scoped one; a universal file gets a
// process-scoped one with its own reason.
func TestDarwinIdentityIsTheAuthenticatedCode(t *testing.T) {
	image, cdhashes := signedImage(threePages, 12, sha256Directory)
	codeEnd := machoHeaderSize64 + 16 + len(threePages)
	base := darwinIdentity(image, cdhashes[0])
	if base.ReuseScope != "artifact" || base.Digest != codeDigest(image, codeEnd) || base.Qualifier != base.Digest {
		t.Fatalf("the signed image: %+v, want artifact scope with digest %s", base, codeDigest(image, codeEnd))
	}
	whole := sha256.Sum256(image)
	if base.Digest == "sha256:"+hex.EncodeToString(whole[:]) {
		t.Fatal("the Darwin digest equals the whole-file digest; it must be the tagged code digest")
	}
	// The SuperBlob index slot of the primary directory rewritten to an
	// alternate slot: outside the CodeDirectory, so the cdhash and pages
	// are unchanged (bid-round-2 M2's counterexample).
	alternate := bytes.Clone(image)
	binary.BigEndian.PutUint32(alternate[codeEnd+12:], csSlotAlternateFirst)
	same := map[string][]byte{
		"an index slot rewritten": alternate,
		"one byte appended":       append(bytes.Clone(image), 0),
		"a page appended":         append(bytes.Clone(image), bytes.Repeat([]byte{'x'}, 4096)...),
	}
	for name, candidate := range same {
		if bytes.Equal(candidate, image) {
			t.Fatalf("%s: the fixture did not change the file", name)
		}
		got := darwinIdentity(candidate, cdhashes[0])
		if got.ReuseScope != "artifact" || got.Digest != base.Digest {
			t.Errorf("%s: %+v, want the same artifact digest %s", name, got, base.Digest)
		}
	}
	// Different code under the running image's cdhash: one code byte
	// changed, or another build altogether.
	tampered := bytes.Clone(image)
	tampered[4096]++
	other, _ := signedImage(bytes.Repeat([]byte("compiler B "), 1000), 12, sha256Directory)
	for name, candidate := range map[string][]byte{"a code byte changed": tampered, "another build": other} {
		got := darwinIdentity(candidate, cdhashes[0])
		if got.ReuseScope != "process" || got.Digest != "" || got.Reason != "executing image does not match the kernel's code signature record" {
			t.Errorf("%s: %+v, want process scope, no digest, the unverified reason", name, got)
		}
	}
	// A SHA-1 directory the kernel recorded, with matching pages: process
	// scope, with its own reason (no SHA-1 trust).
	sha1Image, sha1Hashes := signedImage(threePages, 12, signedDirectory{slot: csSlotCodeDirectory, hashType: 1, newHash: sha1.New})
	if got := darwinIdentity(sha1Image, sha1Hashes[0]); got.ReuseScope != "process" || got.Digest != "" || got.Reason != "executing image's code directory does not use full SHA-256 page hashes" {
		t.Errorf("a SHA-1 directory: %+v, want process scope with the weak-directory reason", got)
	}
	// A universal file holding this image as its one slice: a fat header,
	// one arch entry, then the thin image.
	fat := make([]byte, 4096, 4096+len(image))
	binary.BigEndian.PutUint32(fat[0:], fatMagic)
	binary.BigEndian.PutUint32(fat[4:], 1)
	binary.BigEndian.PutUint32(fat[8:], 0x0100000c) // CPU_TYPE_ARM64
	binary.BigEndian.PutUint32(fat[16:], 4096)
	binary.BigEndian.PutUint32(fat[20:], uint32(len(image)))
	binary.BigEndian.PutUint32(fat[24:], 12)
	fat = append(fat, image...)
	for _, magic := range []uint32{fatMagic, fatMagic64} {
		binary.BigEndian.PutUint32(fat, magic)
		if err := verifies(fat, cdhashes[0]); !errors.Is(err, errUniversal) {
			t.Errorf("a universal file (magic %#x): %v, want errUniversal", magic, err)
		}
		if got := darwinIdentity(fat, cdhashes[0]); got.ReuseScope != "process" || got.Digest != "" {
			t.Errorf("a universal file (magic %#x): %+v, want process scope", magic, got)
		}
	}
}

// TestGoLinkedDarwinImageKeepsItsCodeIdentity: a darwin/arm64 binary as the
// Go linker writes and signs it, cross-linked here, verifies against its own
// cdhash and gets artifact scope with the tagged code digest; the same file
// with one byte appended keeps that digest. This runs on every host: it
// reads the file, it never executes it.
func TestGoLinkedDarwinImageKeepsItsCodeIdentity(t *testing.T) {
	dir := t.TempDir()
	for name, text := range map[string]string{
		"go.mod":  "module example.com/signed\n\ngo 1.23\n",
		"main.go": "package main\n\nfunc main() { println(\"signed\") }\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	program := filepath.Join(dir, "signed")
	link := exec.Command("go", "build", "-o", program, ".")
	link.Dir = dir
	link.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	if out, err := link.CombinedOutput(); err != nil {
		t.Fatalf("link a darwin/arm64 binary: %v\n%s", err, out)
	}
	image, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	signature, codeEnd, ok := codeSignature(image)
	if !ok {
		t.Fatal("the Go-linked binary has no code signature")
	}
	directories := codeDirectories(signature)
	if len(directories) == 0 {
		t.Fatal("the Go-linked binary has no CodeDirectory")
	}
	cdhash, ok := directoryHash(directories[0])
	if !ok {
		t.Fatal("the CodeDirectory names no known hash")
	}
	t.Logf("darwin/arm64 binary: %d bytes, signature at %d, %d bytes", len(image), codeEnd, len(signature))
	want := codeDigest(image, int(codeEnd))
	if got := darwinIdentity(image, cdhash[:cdhashSize]); got.ReuseScope != "artifact" || got.Digest != want {
		t.Fatalf("the Go-linked binary against its own cdhash: %+v, want artifact digest %s", got, want)
	}
	if got := darwinIdentity(append(bytes.Clone(image), 0), cdhash[:cdhashSize]); got.ReuseScope != "artifact" || got.Digest != want {
		t.Fatalf("the Go-linked binary with one byte appended: %+v, want artifact digest %s", got, want)
	}
}
