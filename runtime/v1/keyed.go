package v1

// The Go bodies of std sha512, sha1 and hmac (docs/spec/stdlib.md, keyed
// digests; std-spike D3). As digest.go's, each takes and returns strings,
// which hold bytes, and is pure: crypto/sha512, crypto/sha1 and crypto/hmac
// are in PureImports and read no host state.

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
)

// Sha512Sum is the 64 bytes of sha512.Sum512 of the bytes of s.
//
//ergo:pure
func Sha512Sum(s string) string {
	d := sha512.Sum512([]byte(s))
	return string(d[:])
}

// Sha1Sum is the 20 bytes of sha1.Sum of the bytes of s.
//
//ergo:pure
func Sha1Sum(s string) string {
	d := sha1.Sum([]byte(s))
	return string(d[:])
}

// HmacSHA256 is hmac.New(sha256.New, key) over message: 32 bytes.
//
//ergo:pure
func HmacSHA256(key, message string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(message))
	return string(m.Sum(nil))
}

// HmacSHA512 is hmac.New(sha512.New, key) over message: 64 bytes.
//
//ergo:pure
func HmacSHA512(key, message string) string {
	m := hmac.New(sha512.New, []byte(key))
	m.Write([]byte(message))
	return string(m.Sum(nil))
}

// HmacSHA1 is hmac.New(sha1.New, key) over message: 20 bytes.
//
//ergo:pure
func HmacSHA1(key, message string) string {
	m := hmac.New(sha1.New, []byte(key))
	m.Write([]byte(message))
	return string(m.Sum(nil))
}

// HmacEqual is hmac.Equal of the bytes of a and b: whether they are equal,
// in a time that depends on their lengths only, never on where they differ.
//
//ergo:pure
func HmacEqual(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }
