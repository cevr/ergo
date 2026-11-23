package digest

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"math/rand/v2"
	"testing"
)

// digestText is a byte string of every byte value, valid UTF-8 or not,
// with lengths across SHA-256's 64-byte block.
func digestText(r *rand.Rand) string {
	b := make([]byte, r.IntN(140))
	for i := range b {
		b[i] = byte(r.IntN(256))
	}
	return string(b)
}

// BenchmarkDigest is the control of G3 (bench-pending): each body against
// Go's own call on the same 1 KiB of bytes. The bodies only convert and
// call, so the expected gap is the string-to-bytes copy.
func BenchmarkDigest(b *testing.B) {
	s := digestText(rand.New(rand.NewPCG(1, 1)))
	for len(s) < 1024 {
		s += s + "x"
	}
	b.Run("Sha512Sum", func(b *testing.B) {
		for b.Loop() {
			_ = Sha512Sum(s)
		}
	})
	b.Run("GoSum512", func(b *testing.B) {
		for b.Loop() {
			d := sha512.Sum512([]byte(s))
			_ = string(d[:])
		}
	})
	b.Run("Sha1Sum", func(b *testing.B) {
		for b.Loop() {
			_ = Sha1Sum(s)
		}
	})
	b.Run("GoSha1Sum", func(b *testing.B) {
		for b.Loop() {
			d := sha1.Sum([]byte(s))
			_ = string(d[:])
		}
	})
	b.Run("HmacSHA256", func(b *testing.B) {
		for b.Loop() {
			_ = HmacSHA256("key", s)
		}
	})
	b.Run("GoHmacSHA256", func(b *testing.B) {
		for b.Loop() {
			m := hmac.New(sha256.New, []byte("key"))
			m.Write([]byte(s))
			_ = string(m.Sum(nil))
		}
	})
	b.Run("HexEncode", func(b *testing.B) {
		for b.Loop() {
			_ = HexEncode(s)
		}
	})
	b.Run("GoHexEncodeToString", func(b *testing.B) {
		for b.Loop() {
			_ = hex.EncodeToString([]byte(s))
		}
	})
	b.Run("Base64StdEncode", func(b *testing.B) {
		for b.Loop() {
			_ = Base64StdEncode(s)
		}
	})
	b.Run("GoBase64EncodeToString", func(b *testing.B) {
		for b.Loop() {
			_ = base64.StdEncoding.EncodeToString([]byte(s))
		}
	})
}
