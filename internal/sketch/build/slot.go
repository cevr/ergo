package build

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

// The back-end concurrency of a compile (FT5b). A compile's -c runs its
// back end on that many goroutines: more shorten one compile on an idle
// host, and cost CPU. On the IR path (the patched compile, -ergoir) -c=2
// does the least work: a whole `ergo test --count 1` of three examples
// costs 2.4 % to 6.9 % more instructions at -c=4 and 4.8 % to 5.8 % more at
// -c=1, with release flags and with the dev profile alike (FT5b receipt,
// irc.txt and irc-dev.txt). So a compile takes
//
//	-c = min(4, max(2, GOMAXPROCS / compiles in flight))
//
// counting every compile in flight on the host: those of this process and
// those of every other ergo process with the same cache root. One compile
// on an idle host of 4 or more processors keeps cmd/go's -c=4; when half
// as many compiles run as there are processors, or more, each takes the
// cheapest -c=2, and the processors do the compiles instead of the back
// ends' extra work.
//
// A compile in flight is a slot: a file in Root/compiling that its compile
// holds a flock on from before its name appears there until the compile
// ends (made under Root/compiling.tmp and renamed in, so no file in
// Root/compiling is ever unlocked while its owner lives). Counting takes
// each other slot's lock without waiting: a lock that is free is a slot
// whose owner died, and the count removes it. Without flock(2)
// (leasesSupported) and without a root, only this process's compiles are
// counted. -c changes no compiled byte, so no key holds it.

// compileProcs is the processors a compile divides among the compiles in
// flight (runtime.GOMAXPROCS(0); a test sets another).
var compileProcs = func() int { return runtime.GOMAXPROCS(0) }

// inProcess is the compiles this process runs, the count where no slot
// files can be kept.
var inProcess atomic.Int32

// compileSlot takes a slot for one compile under root and returns its -c
// and the release of the slot, which the caller runs when the compile
// ends.
func compileSlot(root string) (c int, release func()) {
	n := int(inProcess.Add(1))
	release = func() { inProcess.Add(-1) }
	if leasesSupported && root != "" {
		if live, free, ok := takeSlot(filepath.Join(root, "compiling")); ok {
			n = live
			done := release
			release = func() { free(); done() }
		}
	}
	return slotConcurrency(compileProcs(), n), release
}

// slotConcurrency is -c for a compile on procs processors with n compiles
// in flight, itself among them.
func slotConcurrency(procs, n int) int {
	return min(4, max(2, procs/max(1, n)))
}

// takeSlot adds a locked slot file to dir and counts the live slots there,
// its own among them, removing each one whose owner died. ok is false
// when no slot could be made; then nothing was added.
func takeSlot(dir string) (live int, free func(), ok bool) {
	tmpDir := dir + ".tmp"
	if os.MkdirAll(dir, 0o755) != nil || os.MkdirAll(tmpDir, 0o755) != nil {
		return 0, nil, false
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, nil, false
	}
	name := "c-" + hex.EncodeToString(nonce[:])
	tmp := filepath.Join(tmpDir, name)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		return 0, nil, false
	}
	if lockShared(f) != nil || os.Rename(tmp, filepath.Join(dir, name)) != nil {
		f.Close()
		os.Remove(tmp)
		return 0, nil, false
	}
	path := filepath.Join(dir, name)
	free = func() {
		os.Remove(path)
		f.Close()
	}
	return 1 + liveSlots(dir, name), free, true
}

// liveSlots counts the slots in dir other than own whose owner holds its
// lock, and removes the others.
func liveSlots(dir, own string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.Name() == own || !strings.HasPrefix(e.Name(), "c-") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			continue // its compile ended
		}
		if tryLockExclusive(f) {
			os.Remove(path) // its owner died
		} else {
			n++
		}
		f.Close()
	}
	return n
}
