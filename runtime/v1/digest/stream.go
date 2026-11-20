package digest

import (
	"crypto/sha256"
	"hash"
	"unsafe"
)

// Sha256Hasher is std sha256's Hasher: a SHA-256 of a text given in parts,
// Go's sha256.New with Write and Sum. It is an owned resource (it
// implements Closer), so the program moves it and its owner's scope
// releases it; a Write is one call of the hash's Write on the bytes of the
// text, with no copy, as hand Go's.
type Sha256Hasher struct{ h hash.Hash }

func (*Sha256Hasher) runtimeOnly() {}

// Sha256New is the body of sha256.New: a Hasher that has hashed nothing.
//
//ergo:wait none
func Sha256New() *Sha256Hasher { return &Sha256Hasher{h: sha256.New()} }

// Write hashes the bytes of s after those h has hashed. It hands the hash
// a view of s's bytes: crypto/sha256's Write only reads its argument and
// keeps none of it (it copies a partial block into its own buffer), so no
// byte of s is written through or held past the call.
//
//ergo:wait none
func (h *Sha256Hasher) Write(s string) {
	h.h.Write(unsafe.Slice(unsafe.StringData(s), len(s)))
}

// Digest is the 32-byte digest of every byte written to h, in order. h is
// unchanged, as Go's Sum leaves it: more writes may follow.
//
//ergo:wait none
func (h *Sha256Hasher) Digest() string {
	var buf [sha256.Size]byte
	return string(h.h.Sum(buf[:0]))
}

// Clone is a Hasher that has hashed what h has, and goes on alone (Go's
// hash.Cloner, which crypto/sha256's digest implements).
//
//ergo:wait none
func (h *Sha256Hasher) Clone() *Sha256Hasher {
	c, err := h.h.(hash.Cloner).Clone()
	if err != nil {
		panic("sha256: a hash that does not clone: " + err.Error())
	}
	return &Sha256Hasher{h: c}
}

// Close releases h: its state is dropped for the collector. The checker
// admits no use of h after it.
//
//ergo:wait none
func (h *Sha256Hasher) Close() { h.h = nil }
