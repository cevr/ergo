package digest

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"math/rand/v2"
	"testing"

	"ergo.local/prototype/internal/lawledger"
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

// digestLaws restate the laws of std hex, base64, sha256, crc32 (G3
// std-digest), sha512 and sha1 (std-spike D3) over their Go bodies.
var digestLaws = map[string]func(r *rand.Rand) (bool, any){
	"hex DecodeInvertsEncode": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		d, err := HexDecode(HexEncode(s))
		return err == nil && d == s, s
	},
	"hex EncodeDoublesLength": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		return len(HexEncode(s)) == 2*len(s), s
	},
	"base64 DecodeInvertsEncode": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		std, err1 := Base64StdDecode(Base64StdEncode(s))
		url, err2 := Base64URLDecode(Base64URLEncode(s))
		return err1 == nil && err2 == nil && std == s && url == s, s
	},
	"base64 EncodedLength": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		return len(Base64StdEncode(s)) == (len(s)+2)/3*4, s
	},
	"sha256 SumIs32Bytes": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		d := sha256.Sum256([]byte(s))
		return len(string(d[:])) == 32, s
	},
	"sha512 SumIs64Bytes": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		return len(Sha512Sum(s)) == 64, s
	},
	"sha1 SumIs20Bytes": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		return len(Sha1Sum(s)) == 20, s
	},
	"crc32 UpdateChains": func(r *rand.Rand) (bool, any) {
		a, b := digestText(r), digestText(r)
		return Crc32Update(crc32.ChecksumIEEE([]byte(a)), b) == crc32.ChecksumIEEE([]byte(a+b)), [2]string{a, b}
	},
	"crc32 UpdateFromZero": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		return Crc32Update(0, s) == crc32.ChecksumIEEE([]byte(s)), s
	},
}

var digestLawStatements = map[string]string{
	"hex DecodeInvertsEncode":    `DecodeInvertsEncode(s string) { try Decode(Encode(s)) } == { s }`,
	"hex EncodeDoublesLength":    `EncodeDoublesLength(s string) { len(Encode(s)) } == { 2 * len(s) }`,
	"base64 DecodeInvertsEncode": `DecodeInvertsEncode(s string) { let std = try Decode(StdEncoding, Encode(StdEncoding, s)) let url = try Decode(URLEncoding, Encode(URLEncoding, s)) std == s && url == s }`,
	"base64 EncodedLength":       `EncodedLength(s string) { len(Encode(StdEncoding, s)) == (len(s) + 2) / 3 * 4 }`,
	"sha256 SumIs32Bytes":        `SumIs32Bytes(s string) { len(Sum(s)) == 32 }`,
	"sha512 SumIs64Bytes":        `SumIs64Bytes(s string) { len(Sum(s)) == 64 }`,
	"sha1 SumIs20Bytes":          `SumIs20Bytes(s string) { len(Sum(s)) == 20 }`,
	"crc32 UpdateChains":         `UpdateChains(a string, b string) { Update(Checksum(a), b) == Checksum(a + b) }`,
	"crc32 UpdateFromZero":       `UpdateFromZero(s string) { Update(0, s) == Checksum(s) }`,
}

// TestStdDigestLaws runs each law of hex, base64, sha256 and crc32 on
// 2,000 seeded byte strings of every byte value (Std laws, stdlib.md).
func TestStdDigestLaws(t *testing.T) {
	laws := map[string]lawledger.Law{}
	for name, holds := range digestLaws {
		laws[name] = lawledger.Law{Statement: digestLawStatements[name], Check: func(t *testing.T) {
			r := rand.New(rand.NewPCG(7, uint64(len(name))))
			for i := range 2000 {
				if ok, c := holds(r); !ok {
					t.Errorf("%s fails on case %d: %q", name, i, c)
					break
				}
			}
		}}
	}
	lawledger.Run(t, laws)
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
