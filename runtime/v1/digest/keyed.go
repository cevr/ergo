package digest

// The Go bodies of std sha512, sha1 and hmac (docs/spec/stdlib.md, keyed
// digests; std-spike D3). As digest.go's, each takes and returns strings,
// which hold bytes, and is pure: crypto/sha512, crypto/sha1 and crypto/hmac
// are in PureImports and read no host state. They live in the digest leaf
// (binsize BS1), so a program that hashes nothing links none of them.

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
)

// Sha512Sum is the 64 bytes of sha512.Sum512 of the bytes of s.
//
//ergo:pure
//ergo:wait none
func Sha512Sum(s string) string {
	d := sha512.Sum512([]byte(s))
	return string(d[:])
}

// Sha1Sum is the 20 bytes of sha1.Sum of the bytes of s.
//
//ergo:pure
//ergo:wait none
func Sha1Sum(s string) string {
	d := sha1.Sum([]byte(s))
	return string(d[:])
}

// HmacSHA256 is hmac.New(sha256.New, key) over message: 32 bytes.
//
//ergo:pure
//ergo:wait none
func HmacSHA256(key, message string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(message))
	return string(m.Sum(nil))
}

// HmacSHA512 is hmac.New(sha512.New, key) over message: 64 bytes.
//
//ergo:pure
//ergo:wait none
func HmacSHA512(key, message string) string {
	m := hmac.New(sha512.New, []byte(key))
	m.Write([]byte(message))
	return string(m.Sum(nil))
}

// HmacSHA1 is hmac.New(sha1.New, key) over message: 20 bytes.
//
//ergo:pure
//ergo:wait none
func HmacSHA1(key, message string) string {
	m := hmac.New(sha1.New, []byte(key))
	m.Write([]byte(message))
	return string(m.Sum(nil))
}

// HmacKeyState is the key of std hmac.Key, hashed once: the states of
// the inner and the outer hash after RFC 2104's key pads (the key, hashed
// first when it is longer than a block, zero-padded to a block and xored
// with 0x36 and with 0x5c), as the hash's MarshalBinary writes them. alg
// is 1, 256 or 512 for SHA-1, SHA-256 or SHA-512. HmacKeyedSum resumes
// both, so a tag under a key made once costs what Go's hmac Reset costs.
// A pure body names no other function, so the hash is picked here and in
// HmacKeyedSum alike.
//
//ergo:pure
//ergo:wait none
func HmacKeyState(alg int, key string) (string, string) {
	newHash, block := sha1.New, 64
	switch alg {
	case 256:
		newHash = sha256.New
	case 512:
		newHash, block = sha512.New, 128
	}
	k := []byte(key)
	if len(k) > block {
		d := newHash()
		d.Write(k)
		k = d.Sum(nil)
	}
	pad := make([]byte, block)
	copy(pad, k)
	for i := range pad {
		pad[i] ^= 0x36
	}
	inner := newHash()
	inner.Write(pad)
	for i := range pad {
		pad[i] ^= 0x36 ^ 0x5c
	}
	outer := newHash()
	outer.Write(pad)
	type marshaler interface{ MarshalBinary() ([]byte, error) }
	a, errA := inner.(marshaler).MarshalBinary()
	b, errB := outer.(marshaler).MarshalBinary()
	if errA != nil || errB != nil {
		panic("hmac: a hash state that does not marshal")
	}
	return string(a), string(b)
}

// HmacKeyedSum is the HMAC of message under the key whose states
// HmacKeyState gave for alg: the inner state resumed and written with
// message, its sum written to the resumed outer state, whose sum is the
// tag (Size(h) bytes). A state HmacKeyState did not give is a defect.
//
//ergo:pure
//ergo:wait none
func HmacKeyedSum(alg int, inner string, outer string, message string) string {
	// Each hash is named where it is made, so Go inlines New and keeps
	// the digest and the state's copy on the stack: a tag allocates its
	// result alone.
	type unmarshaler interface{ UnmarshalBinary([]byte) error }
	var state [256]byte
	var buf [64]byte
	var s []byte
	switch alg {
	case 256:
		d := sha256.New()
		if d.(unmarshaler).UnmarshalBinary(state[:copy(state[:], inner)]) != nil {
			panic("hmac: a key state that hmac.Key did not give")
		}
		d.Write([]byte(message))
		in := d.Sum(buf[:0])
		if d.(unmarshaler).UnmarshalBinary(state[:copy(state[:], outer)]) != nil {
			panic("hmac: a key state that hmac.Key did not give")
		}
		d.Write(in)
		s = d.Sum(buf[:0])
	case 512:
		d := sha512.New()
		if d.(unmarshaler).UnmarshalBinary(state[:copy(state[:], inner)]) != nil {
			panic("hmac: a key state that hmac.Key did not give")
		}
		d.Write([]byte(message))
		in := d.Sum(buf[:0])
		if d.(unmarshaler).UnmarshalBinary(state[:copy(state[:], outer)]) != nil {
			panic("hmac: a key state that hmac.Key did not give")
		}
		d.Write(in)
		s = d.Sum(buf[:0])
	default:
		d := sha1.New()
		if d.(unmarshaler).UnmarshalBinary(state[:copy(state[:], inner)]) != nil {
			panic("hmac: a key state that hmac.Key did not give")
		}
		d.Write([]byte(message))
		in := d.Sum(buf[:0])
		if d.(unmarshaler).UnmarshalBinary(state[:copy(state[:], outer)]) != nil {
			panic("hmac: a key state that hmac.Key did not give")
		}
		d.Write(in)
		s = d.Sum(buf[:0])
	}
	return string(s)
}

// HotpKeyState is std totp.KeyOf's body: the states of HMAC-SHA-1's
// inner and outer hash after RFC 2104's pads of secret (hashed first
// when it is longer than a block), as sha1's MarshalBinary writes them,
// as HmacKeyState(1, secret) gives them. They are bytes, not strings, so
// HotpNumber resumes them with no copy. A pure body names no other
// function, so the pads are written here as in HmacKeyState.
//
//ergo:pure
//ergo:wait none
func HotpKeyState(secret string) ([]byte, []byte) {
	k := []byte(secret)
	if len(k) > sha1.BlockSize {
		s := sha1.Sum(k)
		k = s[:]
	}
	var pad [sha1.BlockSize]byte
	copy(pad[:], k)
	for i := range pad {
		pad[i] ^= 0x36
	}
	inner := sha1.New()
	inner.Write(pad[:])
	for i := range pad {
		pad[i] ^= 0x36 ^ 0x5c
	}
	outer := sha1.New()
	outer.Write(pad[:])
	type marshaler interface{ MarshalBinary() ([]byte, error) }
	a, errA := inner.(marshaler).MarshalBinary()
	b, errB := outer.(marshaler).MarshalBinary()
	if errA != nil || errB != nil {
		panic("totp: a hash state that does not marshal")
	}
	return a, b
}

// HotpNumber is RFC 4226's HOTP value of counter (std totp's step) under
// the key whose states HotpKeyState gave: the HMAC-SHA-1 of counter's
// eight big-endian bytes, the inner state resumed for them and the outer
// for the inner sum, then dynamically truncated: the four bytes at the
// offset the tag's last byte's low four bits give, to 31 bits. The states
// are read in place and the counter and the tag stay on the stack, so it
// copies no state and allocates nothing. A state HotpKeyState did not
// give is a defect.
//
//ergo:pure
//ergo:wait none
func HotpNumber(inner []byte, outer []byte, counter int64) int {
	type unmarshaler interface{ UnmarshalBinary([]byte) error }
	var buf [sha1.Size]byte
	var msg [8]byte
	for i := range msg {
		msg[i] = byte(uint64(counter) >> (56 - 8*i))
	}
	d := sha1.New()
	if d.(unmarshaler).UnmarshalBinary(inner) != nil {
		panic("totp: a key state that totp.KeyOf did not give")
	}
	d.Write(msg[:])
	in := d.Sum(buf[:0])
	if d.(unmarshaler).UnmarshalBinary(outer) != nil {
		panic("totp: a key state that totp.KeyOf did not give")
	}
	d.Write(in)
	h := d.Sum(buf[:0])
	off := h[19] & 15
	return int(uint32(h[off]&0x7f)<<24 | uint32(h[off+1])<<16 | uint32(h[off+2])<<8 | uint32(h[off+3]))
}

// HmacEqual is hmac.Equal of the bytes of a and b: whether they are equal,
// in a time that depends on their lengths only, never on where they differ.
//
//ergo:pure
//ergo:wait none
func HmacEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
