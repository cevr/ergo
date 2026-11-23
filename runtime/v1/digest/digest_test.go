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

// digestLaws restate the SHA-256 streaming laws over their Go bodies.
var digestLaws = map[string]func(r *rand.Rand) (bool, any){
	// Cut points drawn anywhere, past the end and the same cut twice
	// included, so parts of zero bytes and parts across a block run.
	"sha256 PartsAreTheWhole": func(r *rand.Rand) (bool, any) {
		s := digestText(r)
		h, at := Sha256New(), 0
		cuts := make([]int, r.IntN(6))
		for i := range cuts {
			cuts[i] = r.IntN(200) - 20
			n := len(s) + 1
			end := min(at+(cuts[i]%n+n)%n, len(s))
			h.Write(s[at:end])
			at = end
		}
		h.Write(s[at:])
		d := sha256.Sum256([]byte(s))
		return h.Digest() == string(d[:]), [2]any{s, cuts}
	},
	"sha256 NothingIsSumOfEmpty": func(r *rand.Rand) (bool, any) {
		d := sha256.Sum256(nil)
		return Sha256New().Digest() == string(d[:]), nil
	},
	"sha256 DigestKeepsTheHasher": func(r *rand.Rand) (bool, any) {
		a, b := digestText(r), digestText(r)
		h := Sha256New()
		h.Write(a)
		_ = h.Digest()
		h.Write(b)
		d := sha256.Sum256([]byte(a + b))
		return h.Digest() == string(d[:]), [2]string{a, b}
	},
	"sha256 CloneForks": func(r *rand.Rand) (bool, any) {
		a, b, c := digestText(r), digestText(r), digestText(r)
		h := Sha256New()
		h.Write(a)
		k := h.Clone()
		h.Write(b)
		k.Write(c)
		ab, ac := sha256.Sum256([]byte(a+b)), sha256.Sum256([]byte(a+c))
		return h.Digest() == string(ab[:]) && k.Digest() == string(ac[:]), [3]string{a, b, c}
	},
}

var digestLawStatements = map[string]string{
	"sha256 PartsAreTheWhole":     `PartsAreTheWhole(s string, cuts []int) { let h = New() let mut at = 0 for c in cuts { let end = at + (c%(len(s)+1)+len(s)+1)%(len(s)+1) let stop = if end > len(s) { len(s) } else { end } h.Write(s[at:stop]) at = stop } h.Write(s[at:len(s)]) h.Digest() == Sum(s) }`,
	"sha256 NothingIsSumOfEmpty":  `NothingIsSumOfEmpty() { let h = New() h.Digest() == Sum("") }`,
	"sha256 DigestKeepsTheHasher": `DigestKeepsTheHasher(a string, b string) { let h = New() h.Write(a) let _ = h.Digest() h.Write(b) h.Digest() == Sum(a + b) }`,
	"sha256 CloneForks":           `CloneForks(a string, b string, c string) { let h = New() h.Write(a) let k = h.Clone() h.Write(b) k.Write(c) h.Digest() == Sum(a + b) && k.Digest() == Sum(a + c) }`,
}

// TestStdDigestLaws runs the SHA-256 streaming laws on seeded byte strings.
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
